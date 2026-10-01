//! Device-local file destinations. Web URLs use the separate URL opener.
use std::path::{Path, PathBuf};
use std::process::Command;

#[derive(serde::Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Destination {
    Browser,
    DefaultApplication,
}

#[tauri::command]
pub async fn open_local_path(
    absolute_path: String,
    project_roots: Vec<String>,
    destination: Destination,
    browser: String,
    custom_browser_command: Option<String>,
) -> Result<(), String> {
    tauri::async_runtime::spawn_blocking(move || {
        let path = validate_file(&absolute_path, &project_roots)?;
        match destination {
            Destination::DefaultApplication => open_default_application(&path),
            Destination::Browser => {
                if !browser_can_open_path(&path) {
                    return Err("This file format cannot be opened in a browser.".into());
                }
                let url = reqwest::Url::from_file_path(&path)
                    .map_err(|_| "The file path could not be converted to a URL.")?;
                if browser == "system-default" {
                    open_default_browser(url.as_str())
                } else {
                    crate::open_external::spawn_browser(url.as_str(), &browser, custom_browser_command)
                }
            }
        }
    }).await.map_err(|error| error.to_string())?
}

fn validate_file(raw: &str, roots: &[String]) -> Result<PathBuf, String> {
    let path = Path::new(raw);
    if !path.is_absolute() { return Err("An absolute file path is required.".into()); }
    let path = path.canonicalize().map_err(|error| format!("The file is unavailable: {error}"))?;
    if !crate::reveal_in_file_manager::path_under_roots(&path, roots) {
        return Err("The file is outside the available project folders.".into());
    }
    if !path.is_file() { return Err("This destination requires a file.".into()); }
    Ok(path)
}

fn browser_can_open_path(path: &Path) -> bool {
    #[derive(serde::Deserialize)]
    struct Formats { browser: Vec<String> }
    static FORMATS: std::sync::OnceLock<Formats> = std::sync::OnceLock::new();
    let formats = FORMATS.get_or_init(|| serde_json::from_str(include_str!("../../shared/local-file-formats.json")).expect("valid bundled file formats"));
    let extension = path.extension().and_then(|ext| ext.to_str()).unwrap_or("").to_ascii_lowercase();
    formats.browser.contains(&extension)
}

fn run(program: &str, args: &[&str]) -> Result<(), String> {
    let status = Command::new(program).args(args).status().map_err(|error| error.to_string())?;
    if status.success() { Ok(()) } else { Err(format!("{program} exited with {status}")) }
}

fn open_default_application(path: &Path) -> Result<(), String> {
    let path = path.to_str().ok_or("The file path is not valid UTF-8.")?;
    #[cfg(target_os = "macos")]
    { run("open", &[path]) }
    #[cfg(target_os = "linux")]
    { run("xdg-open", &[path]) }
    #[cfg(target_os = "windows")]
    { windows_open_associated(path) }
    #[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
    { let _ = path; Err("Opening files is unavailable on this platform.".into()) }
}

fn open_default_browser(url: &str) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        use objc2_app_kit::NSWorkspace;
        use objc2_foundation::{NSString, NSURL};
        let probe = NSURL::URLWithString(&NSString::from_str("https://example.com"))
            .ok_or("Could not resolve the browser URL.")?;
        let application = NSWorkspace::sharedWorkspace().URLForApplicationToOpenURL(&probe)
            .ok_or("No default browser is configured.")?;
        let application = application.path().ok_or("The default browser has no application path.")?;
        run("open", &["-a", &application.to_string(), url])
    }
    #[cfg(target_os = "linux")]
    {
        let output = Command::new("xdg-settings").args(["get", "default-web-browser"])
            .output().map_err(|error| error.to_string())?;
        if !output.status.success() { return Err("Could not resolve the default browser.".into()); }
        let desktop = String::from_utf8(output.stdout).map_err(|error| error.to_string())?;
        let desktop = desktop.trim();
        if desktop.is_empty() || desktop.starts_with('-') { return Err("No default browser is configured.".into()); }
        run("gtk-launch", &[desktop, url])
    }
    #[cfg(target_os = "windows")]
    { run(&windows_default_browser()?, &[url]) }
    #[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
    { let _ = url; Err("Opening a browser is unavailable on this platform.".into()) }
}

#[cfg(target_os = "windows")]
fn wide(value: &str) -> Vec<u16> { value.encode_utf16().chain(Some(0)).collect() }

#[cfg(target_os = "windows")]
pub(crate) fn windows_open_associated(path: &str) -> Result<(), String> {
    use windows_sys::Win32::System::Com::{CoInitializeEx, CoUninitialize, COINIT_APARTMENTTHREADED, COINIT_DISABLE_OLE1DDE};
    use windows_sys::Win32::UI::Shell::ShellExecuteW;
    use windows_sys::Win32::UI::WindowsAndMessaging::SW_SHOWNORMAL;
    let initialized = unsafe { CoInitializeEx(std::ptr::null(), COINIT_APARTMENTTHREADED as u32 | COINIT_DISABLE_OLE1DDE as u32) };
    if initialized < 0 { return Err(format!("Could not initialize the Windows shell (error {initialized}).")); }
    let path = wide(path);
    let verb = wide("open");
    let result = unsafe { ShellExecuteW(std::ptr::null_mut(), verb.as_ptr(), path.as_ptr(), std::ptr::null(), std::ptr::null(), SW_SHOWNORMAL) } as isize;
    unsafe { CoUninitialize(); }
    if result > 32 { Ok(()) } else { Err(format!("The default application could not open the file (error {result}).")) }
}

#[cfg(target_os = "windows")]
fn windows_default_browser() -> Result<String, String> {
    use windows_sys::Win32::UI::Shell::{AssocQueryStringW, ASSOCF_IS_PROTOCOL, ASSOCSTR_EXECUTABLE};
    let scheme = wide("https");
    let mut length = 0;
    unsafe { AssocQueryStringW(ASSOCF_IS_PROTOCOL, ASSOCSTR_EXECUTABLE, scheme.as_ptr(), std::ptr::null(), std::ptr::null_mut(), &mut length); }
    if length == 0 { return Err("No default browser is configured.".into()); }
    let mut buffer = vec![0; length as usize];
    let result = unsafe { AssocQueryStringW(ASSOCF_IS_PROTOCOL, ASSOCSTR_EXECUTABLE, scheme.as_ptr(), std::ptr::null(), buffer.as_mut_ptr(), &mut length) };
    if result != 0 { return Err("Could not resolve the default browser.".into()); }
    String::from_utf16(&buffer[..length.saturating_sub(1) as usize]).map_err(|error| error.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn browser_formats_are_explicit() {
        for path in ["report.PDF", "page.html", "a.svg", "photo.avif", "notes.txt"] { assert!(browser_can_open_path(Path::new(path))); }
        for path in ["script.sh", "program.exe", "source.ts", "folder", "page.html.exe"] { assert!(!browser_can_open_path(Path::new(path))); }
    }
    #[test]
    fn validates_existing_files_and_roots() {
        let dir = std::env::temp_dir().join(format!("open-path-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir(&dir).unwrap();
        let file = dir.join("a #%é.html");
        std::fs::write(&file, "test").unwrap();
        let roots = vec![dir.to_string_lossy().into_owned()];
        let path = validate_file(file.to_str().unwrap(), &roots).unwrap();
        let url = reqwest::Url::from_file_path(&path).unwrap();
        assert!(url.as_str().contains("a%20%23%25%C3%A9.html"));
        assert!(validate_file(file.to_str().unwrap(), &[]).is_err());
        assert!(validate_file(dir.to_str().unwrap(), &roots).is_err());
        assert!(validate_file("relative.html", &roots).is_err());
        assert!(validate_file(dir.join("missing").to_str().unwrap(), &roots).is_err());
        std::fs::remove_dir_all(dir).unwrap();
    }
    #[cfg(unix)]
    #[test]
    fn symlink_cannot_escape_roots() {
        let dir = std::env::temp_dir().join(format!("open-path-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(dir.join("root")).unwrap();
        std::fs::write(dir.join("outside.html"), "test").unwrap();
        std::os::unix::fs::symlink(dir.join("outside.html"), dir.join("root/link.html")).unwrap();
        assert!(validate_file(dir.join("root/link.html").to_str().unwrap(), &[dir.join("root").to_string_lossy().into_owned()]).is_err());
        std::fs::remove_dir_all(dir).unwrap();
    }
}
