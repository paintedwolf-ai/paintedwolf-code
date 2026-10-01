//! Projects the menu spec built by `src/shortcuts/app-menu-model.ts` into the native application menu.
//! Platform menu items preserve native text-editing behavior.

use serde::Deserialize;
use tauri::menu::{
    AboutMetadata, CheckMenuItemBuilder, Menu, MenuBuilder, MenuItemBuilder, Submenu,
    SubmenuBuilder,
};
use tauri::{AppHandle, Emitter, Manager, Runtime, WebviewWindow};

/// Emitted to the webview when a projected item is chosen; payload is the id.
pub const MENU_COMMAND_EVENT: &str = "menu://command";

#[derive(Debug, Clone, Deserialize)]
pub struct MenuItemSpec {
    /// Stable command id from the contribution registry; round-trips back as the event payload.
    pub id: String,
    pub title: String,
    /// Absent when no bound chord has a faithful accelerator spelling.
    pub accelerator: Option<String>,
    pub enabled: bool,
    /// Present for stateful menu toggles.
    pub checked: Option<bool>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct MenuSectionSpec {
    /// One of the registry's `AppMenuId` values.
    pub menu: String,
    pub title: String,
    /// Groups render in order with a separator between them.
    pub groups: Vec<Vec<MenuItemSpec>>,
}

fn append_groups<'a, R: Runtime>(
    app: &AppHandle<R>,
    mut builder: SubmenuBuilder<'a, R, AppHandle<R>>,
    groups: &[Vec<MenuItemSpec>],
    leading_separator: bool,
) -> tauri::Result<SubmenuBuilder<'a, R, AppHandle<R>>> {
    for (index, group) in groups.iter().enumerate() {
        if group.is_empty() {
            continue;
        }
        if leading_separator || index > 0 {
            builder = builder.separator();
        }
        for item in group {
            if let Some(checked) = item.checked {
                let mut entry = CheckMenuItemBuilder::with_id(item.id.clone(), &item.title)
                    .enabled(item.enabled)
                    .checked(checked);
                if let Some(accelerator) = item.accelerator.as_deref() {
                    entry = entry.accelerator(accelerator);
                }
                builder = builder.item(&entry.build(app)?);
                continue;
            }
            let mut entry =
                MenuItemBuilder::with_id(item.id.clone(), &item.title).enabled(item.enabled);
            if let Some(accelerator) = item.accelerator.as_deref() {
                entry = entry.accelerator(accelerator);
            }
            builder = builder.item(&entry.build(app)?);
        }
    }
    Ok(builder)
}

fn section<'a>(sections: &'a [MenuSectionSpec], menu: &str) -> Option<&'a MenuSectionSpec> {
    sections.iter().find(|s| s.menu == menu)
}

fn groups_of<'a>(sections: &'a [MenuSectionSpec], menu: &str) -> &'a [Vec<MenuItemSpec>] {
    section(sections, menu).map_or(&[], |s| s.groups.as_slice())
}

fn title_of(sections: &[MenuSectionSpec], menu: &str, fallback: &str) -> String {
    section(sections, menu).map_or_else(|| fallback.to_string(), |s| s.title.clone())
}

fn has_items(groups: &[Vec<MenuItemSpec>]) -> bool {
    groups.iter().any(|group| !group.is_empty())
}

fn app_submenu<R: Runtime>(
    app: &AppHandle<R>,
    sections: &[MenuSectionSpec],
) -> tauri::Result<Submenu<R>> {
    let title = title_of(sections, "app", "Painted Wolf Code");
    let builder = SubmenuBuilder::new(app, &title).about(Some(AboutMetadata::default()));
    let builder = append_groups(app, builder, groups_of(sections, "app"), true)?;
    let builder = builder
        .separator()
        .services()
        .separator()
        .hide()
        .hide_others_with_text("Hide others")
        .show_all_with_text("Show all")
        .separator()
        .quit();
    builder.build()
}

fn edit_submenu<R: Runtime>(
    app: &AppHandle<R>,
    sections: &[MenuSectionSpec],
) -> tauri::Result<Submenu<R>> {
    let title = title_of(sections, "edit", "Edit");
    let builder = SubmenuBuilder::new(app, &title)
        .undo()
        .redo()
        .separator()
        .cut()
        .copy()
        .paste()
        .select_all_with_text("Select all");
    append_groups(app, builder, groups_of(sections, "edit"), true)?.build()
}

fn view_submenu<R: Runtime>(
    app: &AppHandle<R>,
    sections: &[MenuSectionSpec],
) -> tauri::Result<Submenu<R>> {
    let groups = groups_of(sections, "view");
    let builder = SubmenuBuilder::new(app, &title_of(sections, "view", "View"));
    let builder = append_groups(app, builder, groups, false)?;
    let builder = if has_items(groups) {
        builder.separator()
    } else {
        builder
    };
    builder.fullscreen_with_text("Enter full screen").build()
}

fn window_submenu<R: Runtime>(
    app: &AppHandle<R>,
    sections: &[MenuSectionSpec],
) -> tauri::Result<Submenu<R>> {
    let title = title_of(sections, "window", "Window");
    let builder = SubmenuBuilder::new(app, &title)
        .minimize()
        .maximize()
        .separator()
        .close_window_with_text("Close window");
    append_groups(app, builder, groups_of(sections, "window"), true)?
        .separator()
        .bring_all_to_front_with_text("Bring all to front")
        .build()
}

/// Menus whose whole content is projected. An empty one is left out of the
/// bar rather than shown with nothing in it.
fn plain_submenu<R: Runtime>(
    app: &AppHandle<R>,
    sections: &[MenuSectionSpec],
    menu: &str,
    fallback: &str,
) -> tauri::Result<Option<Submenu<R>>> {
    let groups = groups_of(sections, menu);
    if !has_items(groups) {
        return Ok(None);
    }
    let builder = SubmenuBuilder::new(app, &title_of(sections, menu, fallback));
    Ok(Some(append_groups(app, builder, groups, false)?.build()?))
}

/// The bar plus the two submenus AppKit is told about once the bar is live.
struct BuiltMenu<R: Runtime> {
    menu: Menu<R>,
    window: Submenu<R>,
    help: Option<Submenu<R>>,
}

fn build_menu<R: Runtime>(
    app: &AppHandle<R>,
    sections: &[MenuSectionSpec],
) -> tauri::Result<BuiltMenu<R>> {
    let file = plain_submenu(app, sections, "file", "File")?;
    let selection = plain_submenu(app, sections, "selection", "Selection")?;
    let go = plain_submenu(app, sections, "go", "Go")?;
    let help = plain_submenu(app, sections, "help", "Help")?;
    let window = window_submenu(app, sections)?;

    let mut builder = MenuBuilder::new(app).item(&app_submenu(app, sections)?);
    if let Some(file) = &file {
        builder = builder.item(file);
    }
    builder = builder.item(&edit_submenu(app, sections)?);
    if let Some(selection) = &selection {
        builder = builder.item(selection);
    }
    builder = builder.item(&view_submenu(app, sections)?);
    if let Some(go) = &go {
        builder = builder.item(go);
    }
    builder = builder.item(&window);
    if let Some(help) = &help {
        builder = builder.item(help);
    }
    Ok(BuiltMenu {
        menu: builder.build()?,
        window,
        help,
    })
}

/// Accelerator changes replace the menu because items lack stable runtime handles.
#[tauri::command]
pub fn set_app_menu<R: Runtime>(
    app: AppHandle<R>,
    window: WebviewWindow<R>,
    sections: Vec<MenuSectionSpec>,
) -> Result<bool, String> {
    // The menu is app-global. A webview may lose focus while its asynchronous
    // projection is queued, so only the still-focused sender may replace it.
    if !window.is_focused().unwrap_or(false) {
        return Ok(false);
    }
    let built = build_menu(&app, &sections).map_err(|e| e.to_string())?;
    app.set_menu(built.menu).map_err(|e| e.to_string())?;
    // The system lists open windows under Window and adds search under Help.
    #[cfg(target_os = "macos")]
    {
        built
            .window
            .set_as_windows_menu_for_nsapp()
            .map_err(|e| e.to_string())?;
        if let Some(help) = &built.help {
            help.set_as_help_menu_for_nsapp()
                .map_err(|e| e.to_string())?;
        }
    }
    #[cfg(not(target_os = "macos"))]
    let _ = (built.window, built.help);
    Ok(true)
}

/// Sends the selected command id to the focused webview.
pub fn on_menu_event<R: Runtime>(app: &AppHandle<R>, id: &str) {
    for window in app.webview_windows().values() {
        if window.is_focused().unwrap_or(false) {
            let _ = window.emit(MENU_COMMAND_EVENT, id.to_string());
            return;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn spec(menu: &str, ids: &[&str]) -> MenuSectionSpec {
        MenuSectionSpec {
            menu: menu.to_string(),
            title: menu.to_string(),
            groups: vec![ids
                .iter()
                .map(|id| MenuItemSpec {
                    id: (*id).to_string(),
                    title: (*id).to_string(),
                    accelerator: None,
                    enabled: true,
                    checked: None,
                })
                .collect()],
        }
    }

    #[test]
    fn lookups_find_the_named_section() {
        let sections = vec![spec("file", &["session.new"]), spec("view", &[])];
        assert_eq!(groups_of(&sections, "file").len(), 1);
        assert_eq!(groups_of(&sections, "view").len(), 1);
        assert!(groups_of(&sections, "help").is_empty());
    }

    #[test]
    fn a_menu_with_only_empty_groups_has_no_items() {
        let empty = MenuSectionSpec {
            menu: "selection".to_string(),
            title: "Selection".to_string(),
            groups: vec![vec![], vec![]],
        };
        assert!(!has_items(&empty.groups));
        assert!(has_items(&spec("selection", &["editor.join"]).groups));
        assert!(!has_items(groups_of(&[], "help")));
    }

    #[test]
    fn missing_section_falls_back_to_the_static_title() {
        let sections = vec![spec("file", &["session.new"])];
        assert_eq!(title_of(&sections, "file", "File"), "file");
        assert_eq!(title_of(&sections, "help", "Help"), "Help");
    }
}
