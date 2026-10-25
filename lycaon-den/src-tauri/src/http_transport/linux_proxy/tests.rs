use super::*;

const TARGET: &str = "https://downloads.paintedwolf.ai/latest.json";

fn reply(proxy: serde_json::Value) -> Vec<u8> {
    serde_json::to_vec(
        &serde_json::json!({"format_version": 1, "target": TARGET, "proxy_url": proxy}),
    )
    .unwrap()
}

#[test]
fn response_requires_exact_protocol_target_and_explicit_direct_result() {
    assert_eq!(
        decode(&reply(serde_json::Value::Null), TARGET).unwrap(),
        None
    );
    let proxy = "http://alice:private-secret@proxy.example:3128";
    assert_eq!(
        decode(&reply(proxy.into()), TARGET).unwrap().as_deref(),
        Some(proxy)
    );
    assert_eq!(
        decode(&reply(proxy.into()), "https://other.example/"),
        Err(ProxyError::InvalidReply)
    );
    for raw in [
        b"{}".as_slice(),
        br#"{"format_version":1,"target":"x"}"#,
        b"[]",
        b"null",
    ] {
        assert_eq!(decode(raw, TARGET), Err(ProxyError::InvalidReply));
    }
    for endpoint in [
        "file:///secret",
        "ftp://proxy.example/",
        "http://proxy.example/path",
        "http://proxy.example/?secret=yes",
    ] {
        assert_eq!(
            decode(&reply(endpoint.into()), TARGET),
            Err(ProxyError::EndpointUnsupported)
        );
    }
    for endpoint in [
        "socks5://proxy.example:1080",
        "socks5h://proxy.example:1080",
        "https://proxy.example:443",
    ] {
        assert!(decode(&reply(endpoint.into()), TARGET).unwrap().is_some());
    }
}

#[test]
fn policy_errors_are_actionable_and_never_contain_credentials() {
    let raw = serde_json::to_vec(&serde_json::json!({"format_version": 1, "target": TARGET,
        "proxy_url": null, "error_code": "automatic_proxy_unsupported"}))
    .unwrap();
    let error = decode(&raw, TARGET).unwrap_err();
    assert_eq!(error, ProxyError::AutomaticUnsupported);
    assert!(error.to_string().contains("manual proxy"));
    let mut value: serde_json::Value = serde_json::from_slice(&raw).unwrap();
    value["proxy_url"] = "http://alice:private-secret@proxy.example:3128".into();
    let error = decode(&serde_json::to_vec(&value).unwrap(), TARGET).unwrap_err();
    assert_eq!(error, ProxyError::InvalidReply);
    assert!(!error.to_string().contains("private-secret"));
}

fn fixture(script: &str) -> tokio::process::Command {
    let mut command = tokio::process::Command::new("/bin/sh");
    command.args(["-c", script]);
    command
}

#[tokio::test]
async fn bounded_child_pipe_handles_direct_policy_failure_and_excess_output() {
    let mut command = fixture("cat >/dev/null; printf '%s' \"$PW_PROXY_TEST_REPLY\"");
    command.env(
        "PW_PROXY_TEST_REPLY",
        String::from_utf8(reply(serde_json::Value::Null)).unwrap(),
    );
    assert_eq!(exchange(command, TARGET).await.unwrap(), None);
    assert_eq!(
        exchange(fixture("cat >/dev/null; exit 2"), TARGET).await,
        Err(ProxyError::HelperUnavailable)
    );
    assert_eq!(
        exchange(fixture("cat >/dev/null; head -c 70000 /dev/zero"), TARGET).await,
        Err(ProxyError::InvalidReply)
    );
}

#[tokio::test]
async fn unavailable_or_overdue_child_never_becomes_direct() {
    assert_eq!(
        exchange(
            tokio::process::Command::new("/nonexistent-paintedwolf-proxy-fixture"),
            TARGET
        )
        .await,
        Err(ProxyError::HelperUnavailable)
    );
    assert_eq!(
        exchange(fixture("cat >/dev/null; exec sleep 30"), TARGET).await,
        Err(ProxyError::HelperUnavailable)
    );
}

#[tokio::test]
async fn selected_authenticated_proxy_is_used_for_the_effective_target() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let proxy = format!("http://alice:secret@{}", listener.local_addr().unwrap());
    let server = tokio::spawn(async move {
        let (mut stream, _) = listener.accept().await.unwrap();
        let mut request = vec![0; 8192];
        let count = stream.read(&mut request).await.unwrap();
        let request = String::from_utf8_lossy(&request[..count]).to_lowercase();
        assert!(request.starts_with("get http://origin.invalid/latest.json http/1.1"));
        assert!(request.contains("proxy-authorization: basic ywxpy2u6c2vjcmv0"));
        stream
            .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
            .await
            .unwrap();
    });
    let endpoint = decode(&reply(proxy.into()), TARGET).unwrap().unwrap();
    let client = crate::http_transport::client_builder()
        .no_proxy()
        .proxy(reqwest::Proxy::all(endpoint).unwrap())
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(5))
        .build()
        .unwrap();
    assert_eq!(
        client
            .get("http://origin.invalid/latest.json")
            .send()
            .await
            .unwrap()
            .text()
            .await
            .unwrap(),
        "ok"
    );
    server.await.unwrap();
}
