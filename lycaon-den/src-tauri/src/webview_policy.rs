use tauri::{
    plugin::{Builder, TauriPlugin},
    Runtime, Url, Webview,
};

fn is_local_dev_origin(url: &Url) -> bool {
    matches!(
        url.host_str(),
        Some("127.0.0.1") | Some("localhost") | Some("[::1]")
    )
}

/// Fail-closed allowlist for top-level webview navigations.
pub fn is_allowed_webview_navigation(url: &Url) -> bool {
    if url.scheme() == "tauri" && url.host_str() == Some("localhost") {
        return true;
    }
    if cfg!(dev) && (url.scheme() == "http" || url.scheme() == "https") && is_local_dev_origin(url)
    {
        return true;
    }
    false
}

pub fn init<R: Runtime>() -> TauriPlugin<R> {
    Builder::new("webview-policy")
        .on_navigation(|_webview: &Webview<R>, url: &Url| is_allowed_webview_navigation(url))
        .build()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(url: &str) -> Url {
        Url::parse(url).expect("parse test url")
    }

    #[test]
    fn allows_production_app_protocol() {
        assert!(is_allowed_webview_navigation(&parse(
            "tauri://localhost/index.html"
        )));
    }

    #[test]
    fn denies_external_http_in_production_builds() {
        if cfg!(dev) {
            return;
        }
        assert!(!is_allowed_webview_navigation(&parse(
            "https://example.com/"
        )));
        assert!(!is_allowed_webview_navigation(&parse(
            "http://127.0.0.1:8080/"
        )));
    }

    #[test]
    fn local_dev_origin_matcher_accepts_loopback_hosts() {
        assert!(is_local_dev_origin(&parse("http://127.0.0.1:1420/")));
        assert!(is_local_dev_origin(&parse("http://localhost:1420/")));
        assert!(is_local_dev_origin(&parse("http://[::1]:1420/")));
        assert!(!is_local_dev_origin(&parse("https://example.com/")));
    }

    #[test]
    fn denies_non_app_schemes() {
        assert!(!is_allowed_webview_navigation(&parse("file:///etc/passwd")));
        assert!(!is_allowed_webview_navigation(&parse(
            "javascript:alert(1)"
        )));
    }
}
