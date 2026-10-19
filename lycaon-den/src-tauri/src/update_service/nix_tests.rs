use super::*;
use crate::linux_install::{InstalledPackage, PackageFamily, PackageSourceFacts};

#[test]
fn upstream_feed_transitions_cannot_claim_a_nix_source_check_or_grant_mutation() {
    let mut state = NativeUpdateState::new(
        "1.0.2".into(),
        UpdateChannel::Stable,
        InstallSource::SystemPackage,
    );
    state.package = Some(InstalledPackage {
        family: PackageFamily::Nix,
        name: "painted-wolf-code".into(),
        version: "1.0.2".into(),
        update_command: PackageFamily::Nix.update_command("painted-wolf-code"),
        source: PackageSourceFacts::default(),
    });
    for result in [
        Ok(Some(feed::Offer {
            candidate: tests::candidate("1.1.0"),
            published_age_secs: Some(86400 * 5),
        })),
        Err(Failure::CheckFailed.into()),
        Err(Failure::FeedRejected.into()),
        Ok(Some(feed::Offer {
            candidate: tests::candidate("1.2.0-rc.1"),
            published_age_secs: None,
        })),
        Ok(None),
    ] {
        check::apply_offer(&mut state, result, false, 0, 123);
        state.refresh_capabilities(true);
        assert_eq!(
            state.package.as_ref().unwrap().source,
            PackageSourceFacts::default()
        );
        assert!(state.capabilities.can_check);
        assert!(!state.capabilities.can_download);
        assert!(!state.capabilities.can_restart_to_update);
        assert!(!state.capabilities.can_install_automatically);
        assert!(state
            .package
            .as_ref()
            .unwrap()
            .update_command
            .starts_with("Update the Nix"));
    }
    assert_eq!(state.discovery, Discovery::UpToDate);
    assert!(state.candidate.is_none());
}
