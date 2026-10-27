//! D3D11 renderer for a composition swap chain: one instanced quad shader draws rounded rectangles,
//! outlines and glyphs. Glyphs are rasterised by DirectWrite into a single R8 atlas, on demand.
//! Frames are drawn only when the caller asks for one; nothing here runs on a timer.

use std::{collections::HashMap, ffi::c_void, mem::ManuallyDrop};

use windows::{
    core::*,
    Win32::{
        Foundation::*,
        Graphics::{
            Direct3D::{Fxc::*, *},
            Direct3D11::*,
            DirectComposition::*,
            DirectWrite::*,
            Dxgi::{Common::*, *},
        },
    },
};

const SHADER: &str = r#"
cbuffer C : register(b0) { float2 screen; float2 pad; };
struct Inst { float4 rect : RECT; float4 col : COLOR; float4 uv : UV; float4 p : PARAMS; };
struct Out {
    float4 pos : SV_Position; float2 loc : LOC; float2 uv : TEXCOORD;
    nointerpolation float4 col : COLOR; nointerpolation float4 p : PARAMS; nointerpolation float2 half_size : HALF;
    nointerpolation float4 seg : SEG;
};
Out vs(uint vid : SV_VertexID, Inst i) {
    float2 c = float2(vid & 1, vid >> 1);
    float2 px = i.rect.xy + c * i.rect.zw;
    Out o;
    o.pos = float4(px.x / screen.x * 2 - 1, 1 - px.y / screen.y * 2, 0, 1);
    o.loc = (c - 0.5) * i.rect.zw;
    o.half_size = i.rect.zw * 0.5;
    o.uv = lerp(i.uv.xy, i.uv.zw, c);
    o.col = i.col; o.p = i.p;
    float2 mid = i.rect.xy + i.rect.zw * 0.5;
    o.seg = float4(i.uv.xy - mid, i.uv.zw - mid);
    return o;
}
Texture2D atlas : register(t0);
SamplerState smp : register(s0);
float4 ps(Out i) : SV_Target {
    float a;
    if (i.p.y > 1.5) {
        float2 pa = i.loc - i.seg.xy, ba = i.seg.zw - i.seg.xy;
        if (i.p.z > 0) {
            // Flat ends whose edge coverage adds up to 1 across a joint, so a translucent polyline has no beads.
            float len = max(length(ba), 1e-4);
            float u = dot(pa, ba) / len;
            float d = abs(pa.x * ba.y - pa.y * ba.x) / len - i.p.x * 0.5;
            a = saturate(0.5 - d) * saturate(u + 0.5) * saturate(len - u + 0.5);
        } else {
            float t = clamp(dot(pa, ba) / max(dot(ba, ba), 1e-4), 0, 1);
            a = saturate(0.5 - (length(pa - ba * t) - i.p.x * 0.5));
        }
    } else if (i.p.y > 0.5) {
        a = atlas.Sample(smp, i.uv).r;
    } else {
        float2 q = abs(i.loc) - i.half_size + i.p.x;
        float d = length(max(q, 0)) + min(max(q.x, q.y), 0) - i.p.x;
        a = saturate(0.5 - d);
        if (i.p.z > 0) a *= saturate(d + i.p.z + 0.5);
    }
    a *= i.col.a;
    return float4(i.col.rgb * a, a);
}
"#;

/// One quad. `p` is (corner radius, 1 for a glyph, outline width, unused); colours are straight alpha.
#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct Inst {
    rect: [f32; 4],
    col: [f32; 4],
    uv: [f32; 4],
    p: [f32; 4],
}

struct Font {
    /// Another font to take a character from when this one has none, the way a browser does for Chinese.
    fallback: Option<usize>,
    face: IDWriteFontFace,
    upem: f32,
    asc: f32,
    desc: f32,
}

#[derive(Clone, Copy)]
struct Glyph {
    uv: [f32; 4],
    w: f32,
    h: f32,
    left: f32,
    top: f32,
}

const ATLAS: u32 = 1024;

pub struct Gfx {
    ctx: ID3D11DeviceContext,
    dev: ID3D11Device,
    swap: IDXGISwapChain1,
    rtv: Option<ID3D11RenderTargetView>,
    vs: ID3D11VertexShader,
    ps: ID3D11PixelShader,
    layout: ID3D11InputLayout,
    cb: ID3D11Buffer,
    ibuf: ID3D11Buffer,
    atlas: ID3D11Texture2D,
    atlas_srv: ID3D11ShaderResourceView,
    sampler: ID3D11SamplerState,
    blend: ID3D11BlendState,
    raster: ID3D11RasterizerState,
    _dcomp: IDCompositionDevice,
    _target: Option<IDCompositionTarget>,
    _visual: Option<IDCompositionVisual>,
    /// The text drawn in this frame and where, for the accessibility tree: (x, y, width, height, text).
    texts: Vec<(f32, f32, f32, f32, String)>,
    /// (font, fallback) pairs, to set again after the device is rebuilt.
    fallbacks: Vec<(usize, usize)>,
    /// What `load_font` was asked for, to ask again after the device is rebuilt.
    font_specs: Vec<(Vec<String>, i32)>,
    dw: IDWriteFactory,
    fonts: Vec<Font>,
    glyphs: HashMap<(usize, u16, u32), Glyph>,
    shelf: (u32, u32, u32),
    pub size: (u32, u32),
    inst: Vec<Inst>,
    /// Where the scissor rectangle changes: (first instance, [left, top, right, bottom]); empty means no clipping.
    clips: Vec<(usize, [i32; 4])>,
}

const MAX_INST: usize = 8192;

fn compile(entry: PCSTR, target: PCSTR) -> Result<ID3DBlob> {
    let mut blob = None;
    let mut err = None;
    unsafe {
        let r = D3DCompile(SHADER.as_ptr() as *const c_void, SHADER.len(), PCSTR::null(), None, None, entry, target, 0, 0, &mut blob, Some(&mut err));
        if let (Err(e), Some(m)) = (&r, &err) {
            let text = std::slice::from_raw_parts(m.GetBufferPointer() as *const u8, m.GetBufferSize());
            return Err(Error::new(e.code(), String::from_utf8_lossy(text).to_string()));
        }
        r?;
    }
    Ok(blob.unwrap())
}

fn bytes(b: &ID3DBlob) -> &[u8] {
    unsafe { std::slice::from_raw_parts(b.GetBufferPointer() as *const u8, b.GetBufferSize()) }
}

impl Gfx {
    pub fn new(hwnd: HWND, w: u32, h: u32) -> Result<Self> {
        unsafe {
            let mut dev = None;
            let mut ctx = None;
            D3D11CreateDevice(None::<&IDXGIAdapter>, D3D_DRIVER_TYPE_HARDWARE, HMODULE::default(), D3D11_CREATE_DEVICE_BGRA_SUPPORT, None, D3D11_SDK_VERSION, Some(&mut dev), None, Some(&mut ctx))?;
            let (dev, ctx) = (dev.unwrap(), ctx.unwrap());
            let dxgi: IDXGIDevice = dev.cast()?;
            let factory: IDXGIFactory2 = dxgi.GetAdapter()?.GetParent()?;

            // Premultiplied alpha: pixels left at alpha 0 show whatever DWM puts behind the window (Mica).
            let desc = DXGI_SWAP_CHAIN_DESC1 {
                Width: w,
                Height: h,
                Format: DXGI_FORMAT_B8G8R8A8_UNORM,
                SampleDesc: DXGI_SAMPLE_DESC { Count: 1, Quality: 0 },
                BufferUsage: DXGI_USAGE_RENDER_TARGET_OUTPUT,
                BufferCount: 2,
                Scaling: DXGI_SCALING_STRETCH,
                SwapEffect: DXGI_SWAP_EFFECT_FLIP_SEQUENTIAL,
                AlphaMode: DXGI_ALPHA_MODE_PREMULTIPLIED,
                ..Default::default()
            };
            let swap = factory.CreateSwapChainForComposition(&dev, &desc, None)?;
            let dcomp: IDCompositionDevice = DCompositionCreateDevice(&dxgi)?;
            let target = dcomp.CreateTargetForHwnd(hwnd, false)?;
            let visual = dcomp.CreateVisual()?;
            visual.SetContent(&swap)?;
            target.SetRoot(&visual)?;
            dcomp.Commit()?;

            let vsb = compile(s!("vs"), s!("vs_5_0"))?;
            let psb = compile(s!("ps"), s!("ps_5_0"))?;
            let mut vs = None;
            dev.CreateVertexShader(bytes(&vsb), None, Some(&mut vs))?;
            let mut ps = None;
            dev.CreatePixelShader(bytes(&psb), None, Some(&mut ps))?;
            let el = |name: PCSTR, off: u32| D3D11_INPUT_ELEMENT_DESC {
                SemanticName: name,
                SemanticIndex: 0,
                Format: DXGI_FORMAT_R32G32B32A32_FLOAT,
                InputSlot: 0,
                AlignedByteOffset: off,
                InputSlotClass: D3D11_INPUT_PER_INSTANCE_DATA,
                InstanceDataStepRate: 1,
            };
            let elems = [el(s!("RECT"), 0), el(s!("COLOR"), 16), el(s!("UV"), 32), el(s!("PARAMS"), 48)];
            let mut layout = None;
            dev.CreateInputLayout(&elems, bytes(&vsb), Some(&mut layout))?;

            let mut cb = None;
            dev.CreateBuffer(&D3D11_BUFFER_DESC { ByteWidth: 16, Usage: D3D11_USAGE_DEFAULT, BindFlags: D3D11_BIND_CONSTANT_BUFFER.0 as u32, ..Default::default() }, None, Some(&mut cb))?;
            let mut ibuf = None;
            dev.CreateBuffer(
                &D3D11_BUFFER_DESC {
                    ByteWidth: (MAX_INST * std::mem::size_of::<Inst>()) as u32,
                    Usage: D3D11_USAGE_DYNAMIC,
                    BindFlags: D3D11_BIND_VERTEX_BUFFER.0 as u32,
                    CPUAccessFlags: D3D11_CPU_ACCESS_WRITE.0 as u32,
                    ..Default::default()
                },
                None,
                Some(&mut ibuf),
            )?;
            let mut atlas = None;
            dev.CreateTexture2D(
                &D3D11_TEXTURE2D_DESC {
                    Width: ATLAS,
                    Height: ATLAS,
                    MipLevels: 1,
                    ArraySize: 1,
                    Format: DXGI_FORMAT_R8_UNORM,
                    SampleDesc: DXGI_SAMPLE_DESC { Count: 1, Quality: 0 },
                    Usage: D3D11_USAGE_DEFAULT,
                    BindFlags: D3D11_BIND_SHADER_RESOURCE.0 as u32,
                    ..Default::default()
                },
                None,
                Some(&mut atlas),
            )?;
            let atlas = atlas.unwrap();
            let mut atlas_srv = None;
            dev.CreateShaderResourceView(&atlas, None, Some(&mut atlas_srv))?;
            let mut sampler = None;
            dev.CreateSamplerState(
                &D3D11_SAMPLER_DESC {
                    Filter: D3D11_FILTER_MIN_MAG_MIP_POINT,
                    AddressU: D3D11_TEXTURE_ADDRESS_CLAMP,
                    AddressV: D3D11_TEXTURE_ADDRESS_CLAMP,
                    AddressW: D3D11_TEXTURE_ADDRESS_CLAMP,
                    ComparisonFunc: D3D11_COMPARISON_NEVER,
                    MaxLOD: f32::MAX,
                    ..Default::default()
                },
                Some(&mut sampler),
            )?;
            let mut bd = D3D11_BLEND_DESC::default();
            bd.RenderTarget[0] = D3D11_RENDER_TARGET_BLEND_DESC {
                BlendEnable: true.into(),
                SrcBlend: D3D11_BLEND_ONE,
                DestBlend: D3D11_BLEND_INV_SRC_ALPHA,
                BlendOp: D3D11_BLEND_OP_ADD,
                SrcBlendAlpha: D3D11_BLEND_ONE,
                DestBlendAlpha: D3D11_BLEND_INV_SRC_ALPHA,
                BlendOpAlpha: D3D11_BLEND_OP_ADD,
                RenderTargetWriteMask: D3D11_COLOR_WRITE_ENABLE_ALL.0 as u8,
            };
            let mut blend = None;
            dev.CreateBlendState(&bd, Some(&mut blend))?;
            let mut raster = None;
            dev.CreateRasterizerState(&D3D11_RASTERIZER_DESC { FillMode: D3D11_FILL_SOLID, CullMode: D3D11_CULL_NONE, DepthClipEnable: true.into(), ScissorEnable: true.into(), ..Default::default() }, Some(&mut raster))?;

            let dw: IDWriteFactory = DWriteCreateFactory(DWRITE_FACTORY_TYPE_SHARED)?;
            let mut g = Gfx {
                ctx,
                dev,
                swap,
                rtv: None,
                vs: vs.unwrap(),
                ps: ps.unwrap(),
                layout: layout.unwrap(),
                cb: cb.unwrap(),
                ibuf: ibuf.unwrap(),
                atlas,
                atlas_srv: atlas_srv.unwrap(),
                sampler: sampler.unwrap(),
                blend: blend.unwrap(),
                raster: raster.unwrap(),
                _dcomp: dcomp,
                _target: Some(target),
                _visual: Some(visual),
                font_specs: Vec::new(),
                fallbacks: Vec::new(),
                texts: Vec::new(),
                dw,
                fonts: Vec::new(),
                glyphs: HashMap::new(),
                shelf: (1, 1, 0),
                size: (w, h),
                inst: Vec::with_capacity(512),
                clips: Vec::new(),
            };
            g.make_rtv()?;
            Ok(g)
        }
    }

    fn make_rtv(&mut self) -> Result<()> {
        unsafe {
            let buf: ID3D11Texture2D = self.swap.GetBuffer(0)?;
            let mut rtv = None;
            self.dev.CreateRenderTargetView(&buf, None, Some(&mut rtv))?;
            self.rtv = rtv;
        }
        Ok(())
    }

    pub fn resize(&mut self, w: u32, h: u32) -> Result<()> {
        if (w, h) == self.size || w == 0 || h == 0 {
            return Ok(());
        }
        self.rtv = None;
        unsafe { self.swap.ResizeBuffers(0, w, h, DXGI_FORMAT_UNKNOWN, DXGI_SWAP_CHAIN_FLAG(0))? };
        self.size = (w, h);
        self.make_rtv()
    }

    /// Forget every rasterised glyph; call when the scale changes.
    pub fn reset_glyphs(&mut self) {
        self.glyphs.clear();
        self.shelf = (1, 1, 0);
    }

    /// Build the device, the swap chain and every cache again, after the graphics adapter was reset or
    /// removed. Fonts keep their numbers.
    pub fn recover(&mut self, canvas: HWND) -> Result<()> {
        let specs = std::mem::take(&mut self.font_specs);
        let fallbacks = std::mem::take(&mut self.fallbacks);
        let (w, h) = self.size;
        // The window can have one composition target at a time: let go of the old one first.
        self._target = None;
        self._visual = None;
        self.rtv = None;
        *self = Gfx::new(canvas, w, h)?;
        for (families, weight) in specs {
            let names: Vec<&str> = families.iter().map(String::as_str).collect();
            self.load_font(&names, DWRITE_FONT_WEIGHT(weight))?;
        }
        for (font, fb) in fallbacks {
            self.set_fallback(font, fb);
        }
        Ok(())
    }

    /// First installed family of `families`, at `weight`. Returns its index for `text`.
    pub fn load_font(&mut self, families: &[&str], weight: DWRITE_FONT_WEIGHT) -> Result<usize> {
        self.font_specs.push((families.iter().map(|f| f.to_string()).collect(), weight.0));
        unsafe {
            let mut coll = None;
            self.dw.GetSystemFontCollection(&mut coll, false)?;
            let coll = coll.unwrap();
            for fam in families {
                let wide: Vec<u16> = fam.encode_utf16().chain(Some(0)).collect();
                let (mut idx, mut exists) = (0u32, BOOL(0));
                coll.FindFamilyName(PCWSTR(wide.as_ptr()), &mut idx, &mut exists)?;
                if !exists.as_bool() {
                    continue;
                }
                let font = coll.GetFontFamily(idx)?.GetFirstMatchingFont(weight, DWRITE_FONT_STRETCH_NORMAL, DWRITE_FONT_STYLE_NORMAL)?;
                let face = font.CreateFontFace()?;
                let mut m = DWRITE_FONT_METRICS::default();
                face.GetMetrics(&mut m);
                self.fonts.push(Font { fallback: None, face, upem: m.designUnitsPerEm as f32, asc: m.ascent as f32, desc: m.descent as f32 });
                return Ok(self.fonts.len() - 1);
            }
        }
        Err(Error::new(E_FAIL, "no font family found"))
    }

    fn glyph(&mut self, font: usize, gi: u16, size: f32) -> Option<Glyph> {
        let key = (font, gi, size.to_bits());
        if let Some(g) = self.glyphs.get(&key) {
            return Some(*g);
        }
        let g = self.rasterise(font, gi, size)?;
        self.glyphs.insert(key, g);
        Some(g)
    }

    fn rasterise(&mut self, font: usize, gi: u16, size: f32) -> Option<Glyph> {
        unsafe {
            let adv = 0f32;
            let run = DWRITE_GLYPH_RUN {
                fontFace: ManuallyDrop::new(Some(self.fonts[font].face.clone())),
                fontEmSize: size,
                glyphCount: 1,
                glyphIndices: &gi,
                glyphAdvances: &adv,
                glyphOffsets: std::ptr::null(),
                isSideways: false.into(),
                bidiLevel: 0,
            };
            let analysis = self.dw.CreateGlyphRunAnalysis(&run, 1.0, None, DWRITE_RENDERING_MODE_NATURAL_SYMMETRIC, DWRITE_MEASURING_MODE_NATURAL, 0.0, 0.0);
            drop(ManuallyDrop::into_inner(run.fontFace));
            let analysis = analysis.ok()?;
            let b = analysis.GetAlphaTextureBounds(DWRITE_TEXTURE_CLEARTYPE_3x1).ok()?;
            let (w, h) = ((b.right - b.left).max(0) as u32, (b.bottom - b.top).max(0) as u32);
            if w == 0 || h == 0 {
                return Some(Glyph { uv: [0.0; 4], w: 0.0, h: 0.0, left: 0.0, top: 0.0 });
            }
            let mut rgb = vec![0u8; (w * h * 3) as usize];
            analysis.CreateAlphaTexture(DWRITE_TEXTURE_CLEARTYPE_3x1, &b, &mut rgb).ok()?;
            // There is no ClearType on a transparent surface: keep the coverage, drop the colour fringes.
            let gray: Vec<u8> = rgb.chunks(3).map(|p| ((p[0] as u32 + p[1] as u32 + p[2] as u32) / 3) as u8).collect();

            let (mut x, mut y, mut row) = self.shelf;
            if x + w + 1 > ATLAS {
                x = 1;
                y += row + 1;
                row = 0;
            }
            if y + h + 1 > ATLAS {
                return None;
            }
            let bx = D3D11_BOX { left: x, top: y, front: 0, right: x + w, bottom: y + h, back: 1 };
            self.ctx.UpdateSubresource(&self.atlas, 0, Some(&bx), gray.as_ptr() as *const c_void, w, 0);
            self.shelf = (x + w + 1, y, row.max(h));
            let a = ATLAS as f32;
            Some(Glyph { uv: [x as f32 / a, y as f32 / a, (x + w) as f32 / a, (y + h) as f32 / a], w: w as f32, h: h as f32, left: b.left as f32, top: b.top as f32 })
        }
    }

    /// Characters from `font`, and from its fallback for the ones it has no glyph for: (font, glyph) of each.
    fn shape(&self, font: usize, text: &str) -> Vec<(usize, u16)> {
        let cps: Vec<u32> = text.chars().map(|c| c as u32).collect();
        let primary = self.glyph_indices(font, &cps);
        cps.iter()
            .zip(primary)
            .map(|(&cp, g)| {
                if g == 0 && cp > 0x20 {
                    if let Some(fb) = self.fonts[font].fallback {
                        let g2 = self.glyph_indices(fb, &[cp])[0];
                        if g2 != 0 {
                            return (fb, g2);
                        }
                    }
                }
                (font, g)
            })
            .collect()
    }

    fn glyph_indices(&self, font: usize, cps: &[u32]) -> Vec<u16> {
        let mut gi = vec![0u16; cps.len()];
        unsafe {
            let _ = self.fonts[font].face.GetGlyphIndices(cps.as_ptr(), cps.len() as u32, gi.as_mut_ptr());
        }
        gi
    }

    fn advance(&self, font: usize, gi: u16, size: f32) -> f32 {
        let mut m = [DWRITE_GLYPH_METRICS::default()];
        unsafe {
            let _ = self.fonts[font].face.GetDesignGlyphMetrics(&gi, 1, m.as_mut_ptr(), false);
        }
        m[0].advanceWidth as f32 * size / self.fonts[font].upem
    }

    /// Use `fallback` for the characters `font` has no glyph for.
    pub fn set_fallback(&mut self, font: usize, fallback: usize) {
        self.fonts[font].fallback = Some(fallback);
        self.fallbacks.push((font, fallback));
    }
    /// Width of `text` in the language that is set.
    pub fn measure(&self, font: usize, size: f32, text: &str) -> f32 {
        self.measure_raw(font, size, &plaitway_core::tr(text))
    }

    /// Width of `text` exactly as given: for what the user wrote, which is never translated.
    pub fn measure_raw(&self, font: usize, size: f32, text: &str) -> f32 {
        self.shape(font, text).iter().map(|&(f, g)| self.advance(f, g, size)).sum()
    }

    /// Draw `text` with its x-height box centred on `cy`. Returns the advance width.
    pub fn text(&mut self, font: usize, size: f32, x: f32, cy: f32, text: &str, col: [f32; 4]) -> f32 {
        let shown = plaitway_core::tr(text);
        self.draw_text(font, size, x, cy, &shown, col)
    }

    fn draw_text(&mut self, font: usize, size: f32, x: f32, cy: f32, text: &str, col: [f32; 4]) -> f32 {
        let f = &self.fonts[font];
        let baseline = (cy + (f.asc - f.desc) / 2.0 * size / f.upem).round();
        let mut pen = x;
        for (fnt, g) in self.shape(font, text) {
            if let Some(gl) = self.glyph(fnt, g, size) {
                if gl.w > 0.0 {
                    self.push(Inst { rect: [(pen + gl.left).round(), baseline + gl.top, gl.w, gl.h], col, uv: gl.uv, p: [0.0, 1.0, 0.0, 0.0] });
                }
            }
            pen += self.advance(fnt, g, size);
        }
        // Icons from the symbol fonts have no words to read out.
        if !text.is_empty() && !text.chars().next().is_some_and(|c| ('\u{E000}'..='\u{F8FF}').contains(&c)) {
            self.texts.push((x, cy - size * 0.75, pen - x, size * 1.5, text.to_string()));
        }
        pen - x
    }

    /// The text drawn since the last call.
    pub fn take_texts(&mut self) -> Vec<(f32, f32, f32, f32, String)> {
        std::mem::take(&mut self.texts)
    }

    pub fn rect(&mut self, x: f32, y: f32, w: f32, h: f32, radius: f32, col: [f32; 4]) {
        self.push(Inst { rect: [x, y, w, h], col, uv: [0.0; 4], p: [radius, 0.0, 0.0, 0.0] });
    }

    pub fn outline(&mut self, x: f32, y: f32, w: f32, h: f32, radius: f32, width: f32, col: [f32; 4]) {
        self.push(Inst { rect: [x, y, w, h], col, uv: [0.0; 4], p: [radius, 0.0, width, 0.0] });
    }

    /// A line with round ends, `width` thick.
    pub fn line(&mut self, x0: f32, y0: f32, x1: f32, y1: f32, width: f32, col: [f32; 4]) {
        self.segment(x0, y0, x1, y1, width, col, 0.0);
    }

    /// A line with flat ends, for joining into a polyline.
    pub fn line_flat(&mut self, x0: f32, y0: f32, x1: f32, y1: f32, width: f32, col: [f32; 4]) {
        self.segment(x0, y0, x1, y1, width, col, 1.0);
    }

    fn segment(&mut self, x0: f32, y0: f32, x1: f32, y1: f32, width: f32, col: [f32; 4], flat: f32) {
        let pad = width / 2.0 + 1.0;
        let (lx, ly) = (x0.min(x1) - pad, y0.min(y1) - pad);
        self.push(Inst { rect: [lx, ly, x0.max(x1) + pad - lx, y0.max(y1) + pad - ly], col, uv: [x0, y0, x1, y1], p: [width, 2.0, flat, 0.0] });
    }

    /// Draw nothing outside this rectangle (pixels) from now on, until `unclip`.
    pub fn clip(&mut self, x: f32, y: f32, w: f32, h: f32) {
        self.clips.push((self.inst.len(), [x.floor() as i32, y.floor() as i32, (x + w).ceil() as i32, (y + h).ceil() as i32]));
    }

    pub fn unclip(&mut self) {
        self.clips.push((self.inst.len(), [0, 0, i32::MAX / 2, i32::MAX / 2]));
    }

    /// `text`, cut with an ellipsis when it is wider than `max_w`. Returns the width drawn.
    #[allow(clippy::too_many_arguments)]
    pub fn text_fit(&mut self, font: usize, size: f32, x: f32, cy: f32, text: &str, col: [f32; 4], max_w: f32) -> f32 {
        let shown = self.fit(font, size, text, max_w);
        self.draw_text(font, size, x, cy, &shown, col)
    }

    pub fn fit(&self, font: usize, size: f32, text: &str, max_w: f32) -> String {
        if self.measure_raw(font, size, text) <= max_w {
            return text.to_string();
        }
        let chars: Vec<char> = text.chars().collect();
        let (mut lo, mut hi) = (0usize, chars.len());
        while lo < hi {
            let mid = (lo + hi).div_ceil(2);
            let cut: String = chars[..mid].iter().collect::<String>() + "…";
            if self.measure_raw(font, size, &cut) <= max_w {
                lo = mid;
            } else {
                hi = mid - 1;
            }
        }
        chars[..lo].iter().collect::<String>() + "…"
    }

    fn push(&mut self, i: Inst) {        if self.inst.len() < MAX_INST {
            self.inst.push(i);
        }
    }

    /// Draw what was queued since the last frame, then present.
    pub fn render(&mut self, clear: [f32; 4]) -> Result<()> {
        unsafe {
            let ctx = &self.ctx;
            let (w, h) = (self.size.0 as f32, self.size.1 as f32);
            ctx.OMSetRenderTargets(Some(&[self.rtv.clone()]), None);
            ctx.ClearRenderTargetView(self.rtv.as_ref().unwrap(), &clear);
            ctx.RSSetViewports(Some(&[D3D11_VIEWPORT { TopLeftX: 0.0, TopLeftY: 0.0, Width: w, Height: h, MinDepth: 0.0, MaxDepth: 1.0 }]));
            ctx.RSSetState(&self.raster);
            ctx.OMSetBlendState(&self.blend, None, 0xffff_ffff);

            let screen = [w, h, 0.0, 0.0];
            ctx.UpdateSubresource(&self.cb, 0, None, screen.as_ptr() as *const c_void, 0, 0);
            let mut m = D3D11_MAPPED_SUBRESOURCE::default();
            ctx.Map(&self.ibuf, 0, D3D11_MAP_WRITE_DISCARD, 0, Some(&mut m))?;
            std::ptr::copy_nonoverlapping(self.inst.as_ptr(), m.pData as *mut Inst, self.inst.len());
            ctx.Unmap(&self.ibuf, 0);

            ctx.IASetInputLayout(&self.layout);
            ctx.IASetPrimitiveTopology(D3D_PRIMITIVE_TOPOLOGY_TRIANGLESTRIP);
            let (stride, offset) = (std::mem::size_of::<Inst>() as u32, 0u32);
            ctx.IASetVertexBuffers(0, 1, Some(&Some(self.ibuf.clone())), Some(&stride), Some(&offset));
            ctx.VSSetShader(&self.vs, None);
            ctx.VSSetConstantBuffers(0, Some(&[Some(self.cb.clone())]));
            ctx.PSSetShader(&self.ps, None);
            ctx.PSSetShaderResources(0, Some(&[Some(self.atlas_srv.clone())]));
            ctx.PSSetSamplers(0, Some(&[Some(self.sampler.clone())]));
            // One draw per stretch between scissor changes.
            let mut start = 0usize;
            let mut rect = [0, 0, i32::MAX / 2, i32::MAX / 2];
            let mut stops: Vec<(usize, [i32; 4])> = self.clips.clone();
            stops.push((self.inst.len(), rect));
            for (at, next) in stops {
                let at = at.min(self.inst.len());
                if at > start {
                    ctx.RSSetScissorRects(Some(&[RECT { left: rect[0], top: rect[1], right: rect[2], bottom: rect[3] }]));
                    ctx.DrawInstanced(4, (at - start) as u32, 0, start as u32);
                    start = at;
                }
                rect = next;
            }
            self.inst.clear();
            self.clips.clear();            self.swap.Present(1, DXGI_PRESENT(0)).ok()
        }
    }
}



/// The adapter was reset or taken away (a driver update, a GPU switch, a remote session): the device and
/// everything made with it have to be made again.
pub fn is_device_lost(code: HRESULT) -> bool {
    code == DXGI_ERROR_DEVICE_REMOVED || code == DXGI_ERROR_DEVICE_RESET || code == DXGI_ERROR_DRIVER_INTERNAL_ERROR
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_removed_or_reset_device_is_lost_and_other_errors_are_not() {
        assert!(is_device_lost(DXGI_ERROR_DEVICE_REMOVED));
        assert!(is_device_lost(DXGI_ERROR_DEVICE_RESET));
        assert!(is_device_lost(DXGI_ERROR_DRIVER_INTERNAL_ERROR));
        assert!(!is_device_lost(E_FAIL));
        assert!(!is_device_lost(S_OK));
        assert!(!is_device_lost(DXGI_ERROR_WAS_STILL_DRAWING));
    }
}




