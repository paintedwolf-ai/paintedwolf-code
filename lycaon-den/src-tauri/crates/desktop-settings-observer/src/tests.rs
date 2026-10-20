use super::*;
use std::io::Cursor;
fn frame() -> Frame { Frame { schema_version:1, sequence:1, status:Status::Ready, settings:vec![Setting::Contrast(1)] } }
#[test] fn roundtrip_and_private_protocol() {
    let mut raw=Vec::new(); frame().write(&mut raw).unwrap();
    assert_eq!(read(&mut Cursor::new(&raw),0).unwrap(),Some(frame()));
    assert!(read(&mut Cursor::new(&raw),1).is_err());
    assert!(read(&mut Cursor::new(b"{\"command\":\"anything\"}\n"),0).is_err());
}
#[test] fn bounds_types_and_duplicate_keys_refuse() {
    assert!(read(&mut Cursor::new(vec![b'x';MAX_FRAME+1]),0).is_err());
    for settings in [vec![Setting::TextScale(f64::NAN)],vec![Setting::TextScale(0.0)],vec![Setting::Accent([2.0,0.0,0.0])],vec![Setting::ButtonLayout("x".repeat(257))],vec![Setting::Contrast(0),Setting::Contrast(1)]] {
        let mut value=frame();value.settings=settings;assert!(value.validate().is_err());
    }
    let mut value=frame();value.status=Status::Denied;assert!(value.validate().is_err());
    value.settings.clear();assert!(value.validate().is_ok());
}
#[test] fn status_vocabulary_cannot_convert_denial_to_missing() {
    let mut value=frame();value.settings.clear();value.status=Status::Denied;
    let mut raw=Vec::new();value.write(&mut raw).unwrap();
    assert_eq!(read(&mut Cursor::new(raw),0).unwrap().unwrap().status,Status::Denied);
}
