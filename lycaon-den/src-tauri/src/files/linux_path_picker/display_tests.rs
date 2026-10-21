//! The in-process chooser on a private X11 display: what each response
//! returns, which selections it refuses, and how it sizes and configures.

use super::super::pick_path::{PickFilter, PickOptions, PickTarget};
use super::super::picked_file::GrantMode;
use super::{show, Selection, Shown};
use crate::test_support::private_display::{fixture_command, run_fixture, start_xvfb};
use crate::test_support::TempDir;
use gtk::prelude::*;
use gtk::ResponseType;
use std::path::Path;
use std::sync::mpsc;
use std::time::{Duration, Instant};

const SETTLE: Duration = Duration::from_secs(10);

#[test]
fn x11_display_fixture_exercises_the_protected_chooser() {
    let (_server, display) = start_xvfb();
    run_fixture(
        fixture_command("files::linux_path_picker::display_tests::protected_chooser_child")
            .env("PW_PROTECTED_CHOOSER_FIXTURE", "1")
            .env("DISPLAY", &display)
            .env("GDK_BACKEND", "x11"),
    );
}

#[test]
fn protected_chooser_child() {
    if std::env::var("PW_PROTECTED_CHOOSER_FIXTURE").as_deref() != Ok("1") {
        return;
    }
    gtk::init().expect("initialize GTK on the private display");
    let parent = gtk::Window::new(gtk::WindowType::Toplevel);
    parent.set_default_size(640, 480);
    parent.show_all();
    let root = TempDir::new("protected-chooser");
    let file = root.join("notes é.txt");
    std::fs::write(&file, b"fixture").unwrap();

    folder_accepts_the_current_folder_without_byte_access(&parent, root.path());
    open_returns_read_access_and_refuses_a_folder(&parent, root.path(), &file);
    save_offers_the_name_confirms_overwrite_and_returns_write_access(&parent, root.path());
    closing_the_chooser_is_a_cancellation(&parent);
}

fn options(target: PickTarget) -> PickOptions {
    PickOptions { title: "Choose location".into(), target }
}

/// Runs the main loop until `done` holds or the deadline passes.
fn settle(mut done: impl FnMut() -> bool, what: &str) {
    let deadline = Instant::now() + SETTLE;
    while !done() {
        assert!(Instant::now() < deadline, "{what}");
        gtk::main_iteration_do(false);
        std::thread::sleep(Duration::from_millis(5));
    }
}

fn open(parent: &gtk::Window, target: PickTarget) -> (Shown, mpsc::Receiver<Result<Selection, String>>) {
    let (send, receive) = mpsc::channel();
    let shown = show(parent, options(target), send);
    settle(|| shown.dialog.is_visible(), "chooser never mapped");
    (shown, receive)
}

fn folder_accepts_the_current_folder_without_byte_access(parent: &gtk::Window, root: &Path) {
    let (shown, reply) = open(parent, PickTarget::Folder);
    assert_eq!(shown.chooser.action(), gtk::FileChooserAction::SelectFolder);
    assert!(shown.chooser.is_local_only());
    assert!(!shown.chooser.selects_multiple());
    assert!(shown.dialog.is_modal());
    assert_eq!(shown.dialog.transient_for().as_ref(), Some(parent));
    let (width, height) = shown.dialog.default_size();
    assert!(width <= 760 && height <= 560 && width > 0 && height > 0, "default size {width}x{height}");
    shown.chooser.set_current_folder(root);
    settle(|| shown.chooser.filename().as_deref() == Some(root), "folder never became current");
    shown.dialog.response(ResponseType::Accept);
    assert_eq!(reply.try_recv().unwrap(), Ok(Some((root.to_path_buf(), None))));
}

fn open_returns_read_access_and_refuses_a_folder(parent: &gtk::Window, root: &Path, file: &Path) {
    let filter = PickFilter { name: "Text".into(), extensions: vec!["txt".into(), "md".into()] };
    let (shown, reply) = open(parent, PickTarget::File { filter: Some(filter) });
    assert_eq!(shown.chooser.action(), gtk::FileChooserAction::Open);
    let filters = shown.chooser.list_filters();
    assert_eq!(filters.len(), 1);
    assert_eq!(filters[0].name().as_deref(), Some("Text"));

    // A folder is not a file to read: the chooser stays open with no answer.
    shown.chooser.set_current_folder(root);
    settle(|| shown.chooser.current_folder().as_deref() == Some(root), "folder never became current");
    shown.chooser.unselect_all();
    shown.dialog.response(ResponseType::Accept);
    assert!(reply.try_recv().is_err(), "a folder answered a file pick");
    assert!(shown.dialog.is_visible());

    shown.chooser.select_filename(file);
    settle(|| shown.chooser.filename().as_deref() == Some(file), "file never became selected");
    shown.dialog.response(ResponseType::Accept);
    assert_eq!(reply.try_recv().unwrap(), Ok(Some((file.to_path_buf(), Some(GrantMode::Read)))));
}

fn save_offers_the_name_confirms_overwrite_and_returns_write_access(parent: &gtk::Window, root: &Path) {
    let (shown, reply) = open(parent, PickTarget::Save { file_name: "draft é.md".into() });
    assert_eq!(shown.chooser.action(), gtk::FileChooserAction::Save);
    assert!(shown.chooser.does_overwrite_confirmation());
    assert_eq!(shown.chooser.current_name().as_deref(), Some("draft é.md"));
    shown.chooser.set_current_folder(root);
    let target = root.join("draft é.md");
    settle(|| shown.chooser.filename().as_deref() == Some(target.as_path()), "save location never settled");
    shown.dialog.response(ResponseType::Accept);
    assert_eq!(reply.try_recv().unwrap(), Ok(Some((target, Some(GrantMode::Write)))));
}

fn closing_the_chooser_is_a_cancellation(parent: &gtk::Window) {
    let (shown, reply) = open(parent, PickTarget::Folder);
    shown.dialog.response(ResponseType::Cancel);
    assert_eq!(reply.try_recv().unwrap(), Ok(None));

    let (shown, reply) = open(parent, PickTarget::Folder);
    // The window manager closing it, or its parent going away, also answers.
    unsafe { shown.dialog.destroy() };
    assert_eq!(reply.try_recv().unwrap(), Ok(None));
}
