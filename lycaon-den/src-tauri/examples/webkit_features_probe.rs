//! Reports whether this machine's WebKit offers the features the shell enables for its web views.
//! Run: cargo run --example webkit_features_probe

fn main() {
    #[cfg(target_os = "macos")]
    {
        let mut missing = 0;
        for (key, outcome) in painted_wolf_code_lib::webkit_features::probe_features() {
            match outcome {
                Ok(()) => println!("{key}: available"),
                Err(reason) => {
                    missing += 1;
                    println!("{key}: unavailable ({reason})");
                }
            }
        }
        if missing > 0 {
            std::process::exit(1);
        }
    }
    #[cfg(not(target_os = "macos"))]
    println!("WebKit features apply to macOS web views only");
}
