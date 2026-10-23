//! Read shared host work facts before the explicit user exit transaction preserves resources.
use tauri::{AppHandle, Manager};
use std::time::Duration;
use serde::Deserialize;

#[derive(Deserialize)]
struct WorkFacts {
    active_work_count: u64,
}

fn decode(raw: &[u8]) -> Result<u64, ()> {
    serde_json::from_slice::<WorkFacts>(raw).map(|facts| facts.active_work_count).map_err(|_| ())
}

pub(super) async fn active_work(app: &AppHandle) -> Result<u64, ()> {
    let state = app.state::<crate::SidecarState>();
    let info = state.cached_info().ok_or(())?;
    let facts = fetch(&info).await?;
    let current = state.cached_info().ok_or(())?;
    if (current.port, current.generation, &current.api_token) != (info.port, info.generation, &info.api_token) {
        return Err(());
    }
    Ok(facts)
}

async fn fetch(info: &crate::sidecar::SidecarInfo) -> Result<u64, ()> {
    let client = crate::http_transport::client_builder().no_proxy()
        .redirect(reqwest::redirect::Policy::none()).timeout(Duration::from_secs(3))
        .build().map_err(|_| ())?;
    let mut response = client.get(format!("http://127.0.0.1:{}/v1/settings/power", info.port))
        .bearer_auth(&info.api_token).send().await.map_err(|_| ())?;
    if !response.status().is_success() { return Err(()); }
    let mut raw = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|_| ())? {
        if raw.len() + chunk.len() > 4096 { return Err(()); }
        raw.extend_from_slice(&chunk);
    }
    decode(&raw)
}

fn description(facts: Result<u64, ()>) -> Option<&'static str> {
    match facts {
        Ok(0) => None,
        Ok(_) => Some("Work is still active. Quitting will stop it. You can keep working instead."),
        Err(()) => Some("Painted Wolf Code could not check for active work. Quitting may stop work. You can keep working instead."),
    }
}

pub(super) async fn approve(facts: Result<u64, ()>) -> bool {
    let Some(description) = description(facts) else { return true; };
    rfd::AsyncMessageDialog::new().set_title("Quit Painted Wolf Code?")
        .set_description(description)
        .set_buttons(rfd::MessageButtons::OkCancelCustom("Quit".into(), "Keep working".into()))
        .show().await == rfd::MessageDialogResult::Custom("Quit".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn work_observation_authenticates_and_refuses_failed_redirected_or_unbounded_responses() {
        use std::io::{Read, Write};
        for (status, body, expected) in [
            ("200 OK", r#"{"active_work_count":3}"#.to_string(), Ok(3)),
            ("200 OK", "{}".to_string(), Err(())),
            ("401 Unauthorized", r#"{"active_work_count":0}"#.to_string(), Err(())),
            ("302 Found", r#"{"active_work_count":0}"#.to_string(), Err(())),
            ("200 OK", " ".repeat(4097), Err(())),
        ] {
            let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            let port = listener.local_addr().unwrap().port();
            let server = std::thread::spawn(move || {
                let (mut connection, _) = listener.accept().unwrap();
                connection.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
                let mut request = Vec::new();
                while !request.ends_with(b"\r\n\r\n") {
                    let mut byte = [0];
                    connection.read_exact(&mut byte).unwrap();
                    request.push(byte[0]);
                    assert!(request.len() < 8192);
                }
                let request = String::from_utf8(request).unwrap().to_ascii_lowercase();
                assert!(request.starts_with("get /v1/settings/power http/1.1\r\n"));
                assert!(request.contains("\r\nauthorization: bearer fixture-token\r\n"));
                let response = format!("HTTP/1.1 {status}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len());
                let _ = connection.write_all(response.as_bytes());
            });
            let info = crate::sidecar::SidecarInfo { port, api_token: "fixture-token".into(), generation: 1 };
            assert_eq!(fetch(&info).await, expected);
            server.join().unwrap();
        }
    }

    #[test]
    fn only_explicit_idle_facts_bypass_the_choice() {
        assert_eq!(description(Ok(0)), None);
        assert!(description(Ok(1)).is_some());
        assert!(description(Err(())).is_some());
        assert_ne!(description(Ok(1)), description(Err(())));
    }

    #[test]
    fn active_work_is_typed_and_independent_of_inhibitor_support_or_preference() {
        for raw in [br#"{"active_work_count":2,"supported":false,"keep_awake_while_working":false}"#.as_slice(),
                    br#"{"active_work_count":2,"supported":true,"inhibiting":true}"#.as_slice()] {
            assert_eq!(decode(raw), Ok(2));
        }
        assert_eq!(decode(br#"{"active_work_count":0}"#), Ok(0));
        for raw in [b"{}".as_slice(), br#"{"active_work_count":-1}"#.as_slice(),
                    br#"{"active_work_count":"0"}"#.as_slice(), br#"{"active_work_count":null}"#.as_slice(), b"bad".as_slice()] {
            assert_eq!(decode(raw), Err(()));
        }
    }
}
