//! A real TLS ClientHello catches loss of reqwest's transport feature.
use tokio::io::AsyncReadExt;

#[tokio::test]
async fn https_requests_start_a_tls_handshake() {
    if std::env::var_os("PW_TEST_FRESH_HTTPS_PROCESS").is_none() {
        let results = crate::test_support::TempDir::new("native-https-probe");
        let result = results.join("clienthello");
        let mut child = std::process::Command::new(std::env::current_exe().unwrap())
            .args([
                "--exact",
                "update_service::https_transport_tests::https_requests_start_a_tls_handshake",
                "--nocapture",
            ])
            .env("PW_TEST_FRESH_HTTPS_PROCESS", &result)
            .spawn()
            .expect("start isolated HTTPS transport test");
        let status = child.wait().expect("wait for HTTPS test process");
        assert!(
            status.success(),
            "fresh-process HTTPS transport failed: {status}"
        );
        assert_eq!(
            std::fs::read(&result).expect("read completed HTTPS probe result"),
            b"tls-clienthello-observed",
            "child must execute the HTTPS probe before reporting success"
        );
        return;
    }
    assert!(rustls::crypto::CryptoProvider::get_default().is_none());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let server = tokio::spawn(async move {
        let (mut stream, _) = listener.accept().await.unwrap();
        let mut header = [0; 5];
        stream.read_exact(&mut header).await.unwrap();
        // TLS handshake record, followed by the legacy TLS record version.
        assert_eq!(header[0], 0x16);
        assert_eq!(header[1], 0x03);
    });
    let client = crate::http_transport::client_builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .build()
        .unwrap();
    // The fixture closes after ClientHello; it never supplies a trusted certificate.
    assert!(client
        .get(format!("https://{address}/latest.json"))
        .send()
        .await
        .is_err());
    server.await.unwrap();
    std::fs::write(
        std::env::var_os("PW_TEST_FRESH_HTTPS_PROCESS").unwrap(),
        b"tls-clienthello-observed",
    )
    .expect("record completed HTTPS probe");
}
