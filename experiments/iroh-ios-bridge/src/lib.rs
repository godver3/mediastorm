use std::ffi::c_char;

const VERSION: &[u8] = b"iroh-ios-bridge/0.1.0\0";

#[no_mangle]
pub extern "C" fn iroh_ios_bridge_version() -> *const c_char {
    VERSION.as_ptr().cast()
}

#[no_mangle]
pub extern "C" fn iroh_ios_bridge_smoke_value() -> u32 {
    // Reference Iroh so the static library build proves the dependency links for iOS.
    let _ = iroh::RelayMode::Disabled;
    42
}

