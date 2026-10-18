use super::super::{journal::Phase, tests::fixture};
use super::{relaunch_activated, Reopen};
use crate::update_service::Failure;
use std::cell::RefCell;

#[test]
fn completed_exchange_starts_a_new_process_even_when_the_receipt_cannot_be_written() {
    for record_fails in [false, true] {
        let root = crate::test_support::TempDir::new("update-relaunch-receipt");
        let mut t = fixture(&root);
        t.phase = Phase::Activated;
        t.relaunch = true;
        let target = t.target.clone();
        let events = RefCell::new(Vec::new());
        let result = relaunch_activated(
            &mut t,
            |record| {
                assert_eq!(record.phase, Phase::Activated);
                assert!(!record.relaunch);
                events.borrow_mut().push("record");
                if record_fails {
                    Err(Failure::JournalUnavailable.into())
                } else {
                    Ok(())
                }
            },
            |path, mode| {
                assert_eq!(path, target.as_path());
                assert_eq!(mode, Reopen::NewInstance);
                events.borrow_mut().push("launch");
                Ok(())
            },
        );
        assert_eq!(*events.borrow(), ["record", "launch"]);
        assert_eq!(
            result.err().map(|e| e.code),
            record_fails.then_some(Failure::JournalUnavailable)
        );
    }
}

#[test]
fn helpers_do_not_launch_for_uncommitted_or_consumed_restart_intent() {
    let root = crate::test_support::TempDir::new("update-relaunch-intent");
    for phase in [
        Phase::Prepared,
        Phase::Committed,
        Phase::Activated,
        Phase::StartupConfirmed,
        Phase::Failed,
    ] {
        for relaunch in [false, true] {
            if phase == Phase::Activated && relaunch {
                continue;
            }
            let mut t = fixture(&root);
            t.phase = phase;
            t.relaunch = relaunch;
            relaunch_activated(
                &mut t,
                |_| panic!("must not change the receipt"),
                |_, _| panic!("must not launch an application"),
            )
            .unwrap();
            assert_eq!(t.relaunch, relaunch);
        }
    }
}

#[test]
fn an_os_launch_failure_is_returned_after_the_exchange_receipt() {
    let root = crate::test_support::TempDir::new("update-relaunch-os-failure");
    let mut t = fixture(&root);
    t.phase = Phase::Activated;
    t.relaunch = true;
    let error = relaunch_activated(
        &mut t,
        |_| Ok(()),
        |_, _| Err(Failure::ActivationFailed.into()),
    )
    .unwrap_err();
    assert_eq!(error.code, Failure::ActivationFailed);
    assert_eq!(t.phase, Phase::Activated);
}
