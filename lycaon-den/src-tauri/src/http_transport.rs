pub(crate) fn client_builder() -> reqwest::ClientBuilder {
    let _ = rustls::crypto::ring::default_provider().install_default();
    reqwest::Client::builder()
}

#[cfg(target_os = "linux")]
mod linux_proxy;

/// Redirects are refused so the decision and any credentials stay bound to target.
pub(crate) async fn update_client(
    target: &str,
    timeout: std::time::Duration,
) -> Result<reqwest::Client, String> {
    let builder = client_builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(timeout);
    #[cfg(target_os = "linux")]
    let builder = match linux_proxy::resolve(target)
        .await
        .map_err(|error| error.to_string())?
    {
        None => builder,
        Some(endpoint) => builder.proxy(
            reqwest::Proxy::all(endpoint)
                .map_err(|_| "The configured update proxy is unsupported".to_owned())?,
        ),
    };
    #[cfg(not(target_os = "linux"))]
    let _ = target;
    builder
        .build()
        .map_err(|_| "The update transport could not be initialized".to_owned())
}
