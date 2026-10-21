//! The display server's input idle as the monitor's coverage.

use super::*;

fn compositor(fixture: &Fixture) -> &Compositor {
    fixture.compositor.as_ref().unwrap()
}

#[test]
fn input_idle_steps_away_and_activity_confirms_again() {
    let fixture = Fixture::new(Facts::default(), false);
    let harness = Harness::new(&fixture);
    harness.run(|h| {
        h.monitor.refresh();
        let status = h.monitor.status();
        assert_eq!(
            status.idle.source,
            Some(idle::IdleSource::WaylandInputIdle),
            "{status:?}"
        );
        assert!(h.allowed());
        let timeout = u32::try_from(idle::STEP_AWAY_IDLE.as_millis()).unwrap();
        assert_eq!(
            *compositor(&fixture).requested.lock().unwrap(),
            vec![(timeout, true)]
        );
        compositor(&fixture).report_idle(true);
        h.pump_until("input idle", |h| {
            h.monitor.tick();
            !h.allowed()
        });
        assert_eq!(h.locks(), vec![LockReason::Away]);
        // Idle that persists asks only once.
        h.monitor.tick();
        h.monitor.tick();
        assert_eq!(h.locks(), vec![LockReason::Away]);
        compositor(&fixture).report_idle(false);
        h.pump_until("input resumed", |h| {
            h.monitor.tick();
            h.allowed()
        });
    });
}

#[test]
fn a_version_one_notifier_is_honoured_as_inhibitable_idle() {
    let mut fixture = Fixture::new(Facts::default(), false);
    fixture.compositor = None;
    fixture.compositor = Some(Compositor::start(&fixture.wayland, Some(1)));
    let harness = Harness::new(&fixture);
    harness.run(|h| {
        h.monitor.refresh();
        assert_eq!(
            h.monitor.status().idle.source,
            Some(idle::IdleSource::WaylandIdle)
        );
        assert!(h.allowed());
        let requested = compositor(&fixture).requested.lock().unwrap().clone();
        assert_eq!(requested.len(), 1);
        assert!(
            !requested[0].1,
            "a version-1 notifier offers no input-idle request"
        );
    });
}

#[test]
fn lock_signals_alone_never_confirm_without_an_idle_observer() {
    let mut fixture = Fixture::new(Facts::default(), true);
    fixture.compositor = None;
    let harness = Harness::new(&fixture);
    harness.run(|h| {
        h.monitor.refresh();
        let status = h.monitor.status();
        assert!(status.session.is_some() && status.screensaver_locked == Some(false));
        assert_eq!(
            status.idle.error,
            Some(idle::IdleUnavailable::WaylandUnreachable)
        );
        assert!(!h.allowed());
    });
}

#[test]
fn a_lost_compositor_locks_and_its_return_is_observed_again() {
    let mut fixture = Fixture::new(Facts::default(), false);
    let harness = Harness::new(&fixture);
    harness.run(|h| {
        h.monitor.refresh();
        assert!(h.allowed());
        fixture.compositor = None;
        h.pump_until("compositor loss", |h| {
            h.monitor.tick();
            h.monitor.status().idle.error == Some(idle::IdleUnavailable::Lost)
        });
        assert!(!h.allowed());
        assert!(h.locks().contains(&LockReason::ScreenLocked));
        fixture.compositor = Some(Compositor::start(&fixture.wayland, Some(2)));
        h.pump_until("compositor return", |h| {
            h.monitor.tick();
            h.allowed()
        });
    });
}

#[test]
fn gnome_shell_idle_is_accepted_only_from_the_compositor_process() {
    let mut fixture = Fixture::new(
        Facts {
            mutter_idle_ms: 1_000,
            ..Facts::default()
        },
        false,
    );
    // This process is the compositor, without an idle notifier, and owns the
    // idle monitor on the session bus.
    fixture.compositor = None;
    fixture.compositor = Some(Compositor::start(&fixture.wayland, None));
    let _mutter = Service::mutter(&fixture.desktop, fixture.facts.clone());
    let harness = Harness::new(&fixture);
    harness.run(|h| {
        h.monitor.refresh();
        assert_eq!(
            h.monitor.status().idle.source,
            Some(idle::IdleSource::MutterIdleMonitor)
        );
        assert!(h.allowed());
        fixture.facts.lock().unwrap().mutter_idle_ms = 300_000;
        h.monitor.tick();
        assert!(!h.allowed());
        assert_eq!(h.locks(), vec![LockReason::Away]);
    });
}

#[test]
fn a_session_bus_idle_monitor_from_another_process_is_refused() {
    let mut fixture = Fixture::new(Facts::default(), false);
    fixture.compositor = None;
    let mut child = Command::new(std::env::current_exe().unwrap())
        .args([
            "--exact",
            "vault_lock::linux::tests::compositor::compositor_child",
            "--nocapture",
        ])
        .env(compositor::CHILD_SOCKET_ENV, &fixture.wayland)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let deadline = Instant::now() + Duration::from_secs(10);
    while UnixStream::connect(&fixture.wayland).is_err() {
        assert!(
            Instant::now() < deadline,
            "compositor process never listened"
        );
        thread::sleep(Duration::from_millis(20));
    }
    // This process owns the idle monitor; the compositor is the child.
    let _mutter = Service::mutter(&fixture.desktop, fixture.facts.clone());
    let harness = Harness::new(&fixture);
    harness.run(|h| {
        h.monitor.refresh();
        assert_eq!(
            h.monitor.status().idle.error,
            Some(idle::IdleUnavailable::MutterNotCompositor)
        );
        assert!(!h.allowed());
    });
    let _ = child.kill();
    let _ = child.wait();
}

#[test]
fn an_x11_session_without_its_display_reports_it_unreachable() {
    let fixture = Fixture::new(
        Facts {
            session_type: "x11",
            ..Facts::default()
        },
        false,
    );
    let harness = Harness::new(&fixture);
    harness.run(|h| {
        h.monitor.refresh();
        assert_eq!(
            h.monitor.status().idle.error,
            Some(idle::IdleUnavailable::X11Unreachable)
        );
        assert!(!h.allowed());
    });
}

/// An X server that serves MIT-SCREEN-SAVER; Xvfb ships it.
struct XServer {
    child: Child,
    display: String,
}

impl XServer {
    fn start() -> Self {
        let mut child = Command::new("Xvfb")
            .args(["-displayfd", "1", "-nolisten", "tcp", "-noreset"])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("Xvfb is a Den Linux test dependency");
        let mut number = String::new();
        BufReader::new(child.stdout.take().unwrap())
            .read_line(&mut number)
            .unwrap();
        let display = format!(":{}", number.trim());
        assert!(display.len() > 1, "Xvfb chose no display");
        Self { child, display }
    }
}

impl Drop for XServer {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

#[test]
fn an_x11_session_is_observed_through_the_screen_saver_extension() {
    let mut fixture = Fixture::new(
        Facts {
            session_type: "x11",
            ..Facts::default()
        },
        false,
    );
    let server = XServer::start();
    fixture.x11 = Some(server.display.clone());
    let harness = Harness::new(&fixture);
    harness.run(|h| {
        h.monitor.refresh();
        let status = h.monitor.status();
        assert_eq!(
            status.idle.source,
            Some(idle::IdleSource::X11ScreenSaver),
            "{status:?}"
        );
        assert!(
            !status.idle.idle,
            "a fresh server has had input for less than the period"
        );
        assert!(h.allowed());
        drop(server);
        h.pump_until("X server loss", |h| {
            h.monitor.tick();
            h.monitor.status().idle.error == Some(idle::IdleUnavailable::Lost)
        });
        assert!(!h.allowed());
        assert!(h.locks().contains(&LockReason::ScreenLocked));
    });
}
