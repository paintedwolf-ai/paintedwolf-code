//! Reports which desktop service handles opens and reveals in this session.

fn main() {
    #[cfg(target_os = "linux")]
    {
        use painted_wolf_code_lib::files::linux_desktop_open::{
            open_file, open_uri, show_item,
        };
        use std::path::Path;

        let args: Vec<String> = std::env::args().skip(1).collect();
        let [url, file, directory] = args.as_slice() else {
            eprintln!("usage: linux_desktop_open_probe <url> <file> <directory>");
            std::process::exit(2);
        };
        let mut failures = 0;
        for (label, outcome) in [
            ("open url", open_uri(url)),
            ("open file", open_file(Path::new(file))),
            ("open directory", open_file(Path::new(directory))),
            ("show item", show_item(Path::new(file))),
        ] {
            match outcome {
                Ok(via) => println!("{label}: {via}"),
                Err(reason) => {
                    failures += 1;
                    println!("{label}: failed ({reason})");
                }
            }
        }
        if failures > 0 {
            std::process::exit(1);
        }
    }
    #[cfg(not(target_os = "linux"))]
    {
        println!("this probe reports Linux desktop services only");
    }
}
