use super::drag::take_pending_drags_for;
use super::presentation::valid_workspace_context;
use super::*;

#[test]
fn file_window_identity_is_stable_and_path_sensitive() {
    assert_eq!(
        stable_file_id("p", "r", "src/a.ts"),
        stable_file_id("p", "r", "src/a.ts")
    );
    assert_ne!(
        stable_file_id("p", "r", "src/a.ts"),
        stable_file_id("p", "r", "src/b.ts")
    );
    assert_ne!(
        stable_file_id("p", "r", "src/a.ts"),
        stable_file_id("p", "other", "src/a.ts")
    );
}

#[test]
fn query_values_are_percent_encoded() {
    assert_eq!(percent_encode("src/a file.ts"), "src%2Fa%20file.ts");
}

#[test]
fn an_initiators_pending_drags_are_taken_once() {
    let initiator = "context:files:9001";
    let label = "file:9001:9001";
    PENDING_ITEM_WINDOW_DRAGS.lock().unwrap().insert(
        label.into(),
        PendingItemWindowDrag {
            initiator_label: initiator.into(),
            view: ItemWindowView {
                label: label.into(),
                title: "a.txt".into(),
                view_number: 9001,
                kind: "file".into(),
                project_id: "project-1".into(),
                session_id: None,
                root_id: Some("root-1".into()),
                path: Some("a.txt".into()),
                stage_id: None,
            },
            hovered_target: None,
        },
    );

    let taken = take_pending_drags_for(initiator).expect("take pending drags");

    assert_eq!(taken.len(), 1, "the initiator's pending drag was not taken");
    assert_eq!(taken[0].0, label);
    assert!(
        take_pending_drags_for(initiator)
            .expect("take again")
            .is_empty(),
        "a taken drag must not be handed out twice"
    );
}

#[test]
fn workspace_context_validation_is_shape_driven() {
    assert!(valid_workspace_context(&WorkspaceContext {
        kind: "stage".into(),
        project_id: Some("project-1".into()),
        session_id: None,
        stage_id: Some("files".into()),
        panel_id: None,
    }));
    assert!(!valid_workspace_context(&WorkspaceContext {
        kind: "stage".into(),
        project_id: Some("project-1".into()),
        session_id: Some("session-1".into()),
        stage_id: Some("files".into()),
        panel_id: None,
    }));
}

#[test]
fn view_labels_keep_the_subject_and_distinguish_placements() {
    let first = next_view_label(SESSION_LABEL_PREFIX, "session-1").0;
    let second = next_view_label(SESSION_LABEL_PREFIX, "session-1").0;
    assert!(first.starts_with("session:session-1:"));
    assert_ne!(first, second);
}

#[test]
fn peer_view_numbers_never_take_the_main_windows_number() {
    assert_eq!(FIRST_PEER_VIEW_NUMBER, MAIN_VIEW_NUMBER + 1);
    for _ in 0..3 {
        let (label, number) = next_view_label(SESSION_LABEL_PREFIX, "session-1");
        assert!(number >= FIRST_PEER_VIEW_NUMBER, "{label} took number {number}");
    }
}

#[test]
fn context_views_get_peer_labels() {
    let first = next_view_label(CONTEXT_LABEL_PREFIX, "files").0;
    let second = next_view_label(CONTEXT_LABEL_PREFIX, "files").0;
    assert!(first.starts_with("context:files:"));
    assert_ne!(first, second);
}

#[test]
fn peer_views_sort_by_their_host_assigned_number() {
    let mut views = vec![
        ItemWindowView {
            label: "file:a:7".into(),
            title: "a.rs".into(),
            view_number: 7,
            kind: "file".into(),
            project_id: "p".into(),
            session_id: None,
            root_id: None,
            path: None,
            stage_id: None,
        },
        ItemWindowView {
            label: "context:files:2".into(),
            title: "Files".into(),
            view_number: 2,
            kind: "context".into(),
            project_id: "p".into(),
            session_id: None,
            root_id: None,
            path: None,
            stage_id: None,
        },
    ];
    views.sort_by_key(|view| view.view_number);
    assert_eq!(views[0].view_number, 2);
}
