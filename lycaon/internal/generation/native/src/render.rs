use nativegen::rendering::{RenderRequest, Renderer};
use std::path::Path;

fn run() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<_> = std::env::args().collect();
    if args.len() != 3 {
        return Err("Expected model directory and request JSON".into());
    }
    let file = std::fs::File::open(&args[2])?;
    if !file.metadata()?.is_file() || file.metadata()?.len() > 8 << 20 {
        return Err("Request exceeds its bounds".into());
    }
    let request: RenderRequest = serde_json::from_reader(file)?;
    let renderer = Renderer::load(Path::new(&args[1]))?;
    let tokens = renderer.render(&request, 65536)?;
    println!("{}", serde_json::json!({"prompt_tokens": tokens}));
    Ok(())
}

fn main() {
    if let Err(error) = run() {
        eprintln!("nativegen render: {error}");
        std::process::exit(1);
    }
}
