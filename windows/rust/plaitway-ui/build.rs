// The version resource of the program: the VERSION file of the repository, as Windows shows it in the file's properties.
// It is also what lets the MSI replace an installed Plaitway.exe: Windows Installer keeps a versioned file over a new one that
// has no version, so a Plaitway.exe without it would never replace the one of an older, versioned app.
use std::{env, fs, path::PathBuf};

fn main() {
    let manifest = PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap());
    let version_file = manifest.join("../../../VERSION");
    println!("cargo:rerun-if-changed={}", version_file.display());
    println!("cargo:rerun-if-changed=build.rs");

    let version = fs::read_to_string(&version_file).expect("read the VERSION file of the repository");
    let version = version.trim();
    let parts: Vec<u32> = version.split('.').map(|p| p.parse().expect("VERSION is major.minor.patch")).collect();
    assert_eq!(parts.len(), 3, "VERSION is major.minor.patch");
    let numbers = format!("{},{},{},0", parts[0], parts[1], parts[2]);

    let rc = format!(
        r#"1 VERSIONINFO
FILEVERSION {numbers}
PRODUCTVERSION {numbers}
FILEFLAGSMASK 0x3fL
FILEFLAGS 0x0L
FILEOS 0x40004L
FILETYPE 0x1L
BEGIN
  BLOCK "StringFileInfo"
  BEGIN
    BLOCK "040904b0"
    BEGIN
      VALUE "CompanyName", "KoukeNeko"
      VALUE "FileDescription", "Plaitway"
      VALUE "FileVersion", "{version}.0"
      VALUE "InternalName", "Plaitway"
      VALUE "OriginalFilename", "Plaitway.exe"
      VALUE "ProductName", "Plaitway"
      VALUE "ProductVersion", "{version}"
    END
  END
  BLOCK "VarFileInfo"
  BEGIN
    VALUE "Translation", 0x409, 1200
  END
END
"#
    );
    let out = PathBuf::from(env::var("OUT_DIR").unwrap()).join("version.rc");
    fs::write(&out, rc).unwrap();
    embed_resource::compile(&out, embed_resource::NONE).manifest_required().unwrap();
}
