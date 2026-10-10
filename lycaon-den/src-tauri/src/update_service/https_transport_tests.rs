//! A real TLS ClientHello catches loss of reqwest's transport feature.
use std::time::Duration;
use tokio::io::AsyncReadExt;

#[tokio::test]
async fn https_requests_start_a_tls_handshake() {
    let _ = rustls::crypto::ring::default_provider().install_default();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let server = tokio::spawn(async move {
        let (mut stream, _) = tokio::time::timeout(Duration::from_secs(5), listener.accept())
            .await.unwrap().unwrap();
        let mut header = [0; 5];
        tokio::time::timeout(Duration::from_secs(5), stream.read_exact(&mut header))
            .await.unwrap().unwrap();
        // TLS handshake record, followed by the legacy TLS record version.
        assert_eq!(header[0], 0x16);
        assert_eq!(header[1], 0x03);
    });
    let client = reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(5))
        .build()
        .unwrap();
    // The fixture closes after ClientHello; it never supplies a trusted certificate.
    assert!(client.get(format!("https://{address}/latest.json")).send().await.is_err());
    server.await.unwrap();
}
