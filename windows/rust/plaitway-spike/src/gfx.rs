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
    return o;
}
Texture2D atlas : register(t0);
SamplerState smp : register(s0);
float4 ps(Out i) : SV_Target {
    float a;
    if (i.p.y > 0.5) {
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
    _target: IDCompositionTarget,
    _visual: IDCompositionVisual,
    dw: IDWriteFactory,
    fonts: Vec<Font>,
    glyphs: HashMap<(usize, u16, u32), Glyph>,
    shelf: (u32, u32, u32),
    pub size: (u32, u32),
    inst: Vec<Inst>,
}

const MAX_INST: usize = 4096;

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
            let target = dcomp.CreateTargetForHwnd(hwnd, true)?;
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
            dev.CreateRasterizerState(&D3D11_RASTERIZER_DESC { FillMode: D3D11_FILL_SOLID, CullMode: D3D11_CULL_NONE, DepthClipEnable: true.into(), ..Default::default() }, Some(&mut raster))?;

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
                _target: target,
                _visual: visual,
                dw,
                fonts: Vec::new(),
                glyphs: HashMap::new(),
                shelf: (1, 1, 0),
                size: (w, h),
                inst: Vec::with_capacity(512),
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

    /// First installed family of `families`, at `weight`. Returns its index for `text`.
    pub fn load_font(&mut self, families: &[&str], weight: DWRITE_FONT_WEIGHT) -> Result<usize> {
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
                self.fonts.push(Font { face, upem: m.designUnitsPerEm as f32, asc: m.ascent as f32, desc: m.descent as f32 });
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

    fn indices(&self, font: usize, text: &str) -> Vec<u16> {
        let cps: Vec<u32> = text.chars().map(|c| c as u32).collect();
        let mut gi = vec![0u16; cps.len()];
        unsafe {
            let _ = self.fonts[font].face.GetGlyphIndices(cps.as_ptr(), cps.len() as u32, gi.as_mut_ptr());
        }
        gi
    }

    fn advances(&self, font: usize, gi: &[u16], size: f32) -> Vec<f32> {
        let mut m = vec![DWRITE_GLYPH_METRICS::default(); gi.len()];
        unsafe {
            let _ = self.fonts[font].face.GetDesignGlyphMetrics(gi.as_ptr(), gi.len() as u32, m.as_mut_ptr(), false);
        }
        m.iter().map(|g| g.advanceWidth as f32 * size / self.fonts[font].upem).collect()
    }

    pub fn measure(&self, font: usize, size: f32, text: &str) -> f32 {
        let gi = self.indices(font, text);
        self.advances(font, &gi, size).iter().sum()
    }

    /// Draw `text` with its x-height box centred on `cy`. Returns the advance width.
    pub fn text(&mut self, font: usize, size: f32, x: f32, cy: f32, text: &str, col: [f32; 4]) -> f32 {
        let f = &self.fonts[font];
        let baseline = (cy + (f.asc - f.desc) / 2.0 * size / f.upem).round();
        let gi = self.indices(font, text);
        let adv = self.advances(font, &gi, size);
        let mut pen = x;
        for (g, a) in gi.iter().zip(adv.iter()) {
            if let Some(gl) = self.glyph(font, *g, size) {
                if gl.w > 0.0 {
                    self.push(Inst { rect: [(pen + gl.left).round(), baseline + gl.top, gl.w, gl.h], col, uv: gl.uv, p: [0.0, 1.0, 0.0, 0.0] });
                }
            }
            pen += a;
        }
        pen - x
    }

    pub fn rect(&mut self, x: f32, y: f32, w: f32, h: f32, radius: f32, col: [f32; 4]) {
        self.push(Inst { rect: [x, y, w, h], col, uv: [0.0; 4], p: [radius, 0.0, 0.0, 0.0] });
    }

    pub fn outline(&mut self, x: f32, y: f32, w: f32, h: f32, radius: f32, width: f32, col: [f32; 4]) {
        self.push(Inst { rect: [x, y, w, h], col, uv: [0.0; 4], p: [radius, 0.0, width, 0.0] });
    }

    fn push(&mut self, i: Inst) {
        if self.inst.len() < MAX_INST {
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
            ctx.DrawInstanced(4, self.inst.len() as u32, 0, 0);
            self.inst.clear();
            self.swap.Present(1, DXGI_PRESENT(0)).ok()
        }
    }
}
