fn main() {
    // No protoc on the build machine: use the one vendored by the crate.
    std::env::set_var("PROTOC", protoc_bin_vendored::protoc_bin_path().unwrap());
    let proto = "../../../proto/plaitway/v1/plaitway.proto";
    println!("cargo:rerun-if-changed={proto}");
    tonic_build::configure().build_server(true).compile_protos(&[proto], &["../../../proto", protoc_bin_vendored::include_path().unwrap().to_str().unwrap()]).unwrap();
}


