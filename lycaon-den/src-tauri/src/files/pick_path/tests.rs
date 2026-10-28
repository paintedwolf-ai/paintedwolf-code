//! Shared request admission and selection authority around both native adapters.
use super::*;

fn options(target: PickTarget) -> PickOptions {
    PickOptions { title: "Choose location".into(), target }
}

fn refuses_before_chooser(options: PickOptions) {
    assert!(select_path(options, |_| panic!("invalid request opened a native chooser")).is_err());
}

#[test]
fn malformed_requests_never_open_either_native_adapter() {
    for title in ["nul\0title".into(), "x".repeat(1025)] {
        refuses_before_chooser(PickOptions { title, target: PickTarget::Folder });
    }
    for file_name in ["", ".", "..", "../escape", "/absolute", "nul\0.txt", "directory/"] {
        refuses_before_chooser(options(PickTarget::Save { file_name: file_name.into() }));
    }
    for extension in ["", "*", "a/b", "x?", "x\0"] {
        refuses_before_chooser(options(PickTarget::File { filter: Some(PickFilter {
            name: "Files".into(), extensions: vec![extension.into()],
        }) }));
    }
    for filter in [PickFilter { name: "nul\0name".into(), extensions: vec![] },
                   PickFilter { name: "Files".into(), extensions: vec!["txt".into(); 33] },
                   PickFilter { name: "Files".into(), extensions: vec!["x".repeat(65)] }] {
        refuses_before_chooser(options(PickTarget::File { filter: Some(filter) }));
    }
}

#[test]
fn selected_object_type_defines_single_direction_and_folder_carries_no_byte_grant() {
    let root = crate::test_support::TempDir::new("shared-picker");
    let file = root.join("notes.txt");
    std::fs::write(&file, b"fixture").unwrap();
    let future = root.join("new.txt");
    let folder = select_path(options(PickTarget::Folder), |_| Ok(Some(root.path().to_path_buf()))).unwrap();
    assert_eq!(folder, Some((root.path().to_path_buf(), None)));
    let open = || options(PickTarget::File { filter: None });
    assert_eq!(select_path(open(), |_| Ok(Some(file.clone()))).unwrap(), Some((file.clone(), Some(GrantMode::Read))));
    for invalid in [root.path().to_path_buf(), future.clone(), PathBuf::from("relative")] {
        assert!(select_path(open(), |_| Ok(Some(invalid))).is_err());
    }
    let save = || options(PickTarget::Save { file_name: "draft.txt".into() });
    for path in [&file, &future] {
        assert_eq!(select_path(save(), |_| Ok(Some(path.clone()))).unwrap(), Some((path.clone(), Some(GrantMode::Write))));
    }
    for invalid in [root.path().to_path_buf(), root.join("missing/new.txt"), PathBuf::from("relative")] {
        assert!(select_path(save(), |_| Ok(Some(invalid))).is_err());
    }
    assert!(select_path(options(PickTarget::Folder), |_| Ok(Some(file))).is_err());
}

#[test]
fn cancellation_and_native_failure_never_mint_selection_authority() {
    assert_eq!(select_path(options(PickTarget::Folder), |_| Ok(None)).unwrap(), None);
    assert_eq!(select_path(options(PickTarget::Folder), |_| Err("native refusal".into())), Err("native refusal".into()));
    let filter = PickFilter { name: "Files".into(), extensions: vec!["tar.gz".into(), "md".into()] };
    assert!(select_path(options(PickTarget::File { filter: Some(filter) }), |_| Ok(None)).is_ok());
}
