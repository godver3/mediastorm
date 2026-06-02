# Iroh iOS Bridge Spike

Minimal Rust static library spike for proving that Iroh can be compiled into
the iOS app and called through a narrow C ABI from Swift.

Build device target:

```sh
cargo build --target aarch64-apple-ios
```

Build simulator target:

```sh
cargo build --target aarch64-apple-ios-sim
```

