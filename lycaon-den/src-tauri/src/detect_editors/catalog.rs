/// Catalog entry. Order determines presentation and default selection.
#[allow(dead_code)] // per-platform fields are read only on their target OS
#[derive(Clone, Copy)]
pub(crate) struct EditorCatalogEntry {
    pub(crate) id: &'static str,
    /// Settings label.
    pub(crate) display_name: &'static str,
    /// Bundle filenames in probe priority order.
    pub(crate) macos_app_bundles: &'static [&'static str],
    /// CLI name. Empty selects bundle-only detection.
    pub(crate) macos_cli: &'static str,
    /// Path relative to the per-user program directory.
    pub(crate) windows_install_rel: &'static str,
    pub(crate) windows_clis: &'static [&'static str],
    pub(crate) linux_cli: &'static str,
    /// Desktop file ids in probe priority order, including sandboxed exports.
    pub(crate) linux_desktop_ids: &'static [&'static str],
    /// Tokenized launch arguments with path and line placeholders.
    pub(crate) cli_args: &'static [&'static str],
    /// Tokenized bundle-launch arguments.
    pub(crate) macos_open_args: &'static [&'static str],
    pub(crate) folder_cli_args: &'static [&'static str],
    pub(crate) folder_macos_open_args: &'static [&'static str],
}

const CURSOR_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "cursor",
    display_name: "Cursor",
    macos_app_bundles: &["Cursor.app"],
    macos_cli: "cursor",
    windows_install_rel: "cursor\\Cursor.exe",
    windows_clis: &["cursor.cmd", "cursor.exe"],
    linux_cli: "cursor",
    linux_desktop_ids: &["cursor.desktop"],
    cli_args: &["-g", "{path}:{line}"],
    macos_open_args: &["--args", "-g", "{path}:{line}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const VSCODE_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "vscode",
    display_name: "VS Code",
    macos_app_bundles: &["Visual Studio Code.app"],
    macos_cli: "code",
    windows_install_rel: "Microsoft VS Code\\Code.exe",
    windows_clis: &["code.cmd", "code.exe"],
    linux_cli: "code",
    linux_desktop_ids: &[
        "code.desktop",
        "com.visualstudio.code.desktop",
        "code_code.desktop",
    ],
    cli_args: &["-g", "{path}:{line}"],
    macos_open_args: &["--args", "-g", "{path}:{line}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const VSCODE_INSIDERS_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "vscode-insiders",
    display_name: "VS Code Insiders",
    macos_app_bundles: &["Visual Studio Code - Insiders.app"],
    macos_cli: "code-insiders",
    windows_install_rel: "Microsoft VS Code Insiders\\Code - Insiders.exe",
    windows_clis: &["code-insiders.cmd", "code-insiders.exe"],
    linux_cli: "code-insiders",
    linux_desktop_ids: &[
        "code-insiders.desktop",
        "com.visualstudio.code.insiders.desktop",
        "code-insiders_code-insiders.desktop",
    ],
    cli_args: &["-g", "{path}:{line}"],
    macos_open_args: &["--args", "-g", "{path}:{line}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const VSCODIUM_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "vscodium",
    display_name: "VSCodium",
    macos_app_bundles: &["VSCodium.app"],
    macos_cli: "codium",
    windows_install_rel: "VSCodium\\VSCodium.exe",
    windows_clis: &["codium.cmd", "codium.exe"],
    linux_cli: "codium",
    linux_desktop_ids: &[
        "codium.desktop",
        "com.vscodium.codium.desktop",
        "codium_codium.desktop",
    ],
    cli_args: &["-g", "{path}:{line}"],
    macos_open_args: &["--args", "-g", "{path}:{line}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const WINDSURF_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "windsurf",
    display_name: "Windsurf",
    macos_app_bundles: &["Windsurf.app"],
    macos_cli: "windsurf",
    windows_install_rel: "Windsurf\\Windsurf.exe",
    windows_clis: &["windsurf.cmd", "windsurf.exe"],
    linux_cli: "windsurf",
    linux_desktop_ids: &["windsurf.desktop"],
    cli_args: &["-g", "{path}:{line}"],
    macos_open_args: &["--args", "-g", "{path}:{line}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const SUBLIME_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "sublime",
    display_name: "Sublime Text",
    macos_app_bundles: &["Sublime Text.app"],
    macos_cli: "subl",
    windows_install_rel: "",
    windows_clis: &["subl.exe", "subl"],
    linux_cli: "subl",
    linux_desktop_ids: &[
        "sublime_text.desktop",
        "com.sublimetext.three.desktop",
        "sublime-text_subl.desktop",
    ],
    cli_args: &["{path}:{line}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const BBEDIT_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "bbedit",
    display_name: "BBEdit",
    macos_app_bundles: &["BBEdit.app"],
    macos_cli: "bbedit",
    windows_install_rel: "",
    windows_clis: &[],
    linux_cli: "",
    linux_desktop_ids: &[],
    cli_args: &["+{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const TEXTMATE_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "textmate",
    display_name: "TextMate",
    macos_app_bundles: &["TextMate.app"],
    macos_cli: "mate",
    windows_install_rel: "",
    windows_clis: &[],
    linux_cli: "",
    linux_desktop_ids: &[],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const COTEDITOR_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "coteditor",
    display_name: "CotEditor",
    macos_app_bundles: &["CotEditor.app"],
    macos_cli: "cot",
    windows_install_rel: "",
    windows_clis: &[],
    linux_cli: "",
    linux_desktop_ids: &[],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const NOVA_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "nova",
    display_name: "Nova",
    macos_app_bundles: &["Nova.app"],
    macos_cli: "",
    windows_install_rel: "",
    windows_clis: &[],
    linux_cli: "",
    linux_desktop_ids: &[],
    cli_args: &["{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const ZED_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "zed",
    display_name: "Zed",
    macos_app_bundles: &["Zed.app"],
    macos_cli: "zed",
    windows_install_rel: "",
    windows_clis: &["zed.exe"],
    linux_cli: "zed",
    linux_desktop_ids: &[
        "dev.zed.Zed.desktop",
        "zed.desktop",
    ],
    cli_args: &["{path}:{line}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const XCODE_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "xcode",
    display_name: "Xcode",
    macos_app_bundles: &["Xcode.app"],
    macos_cli: "xed",
    windows_install_rel: "",
    windows_clis: &[],
    linux_cli: "",
    linux_desktop_ids: &[],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

pub(super) const INTELLIJ_IDEA_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "intellij-idea",
    display_name: "IntelliJ IDEA",
    macos_app_bundles: &[
        "IntelliJ IDEA.app",
        "IntelliJ IDEA Ultimate.app",
        "IntelliJ IDEA CE.app",
    ],
    macos_cli: "idea",
    windows_install_rel: "",
    windows_clis: &["idea.exe", "idea64.exe"],
    linux_cli: "idea",
    linux_desktop_ids: &[
        "jetbrains-idea.desktop",
        "jetbrains-idea-ce.desktop",
        "intellij-idea-ultimate_intellij-idea-ultimate.desktop",
        "intellij-idea-community_intellij-idea-community.desktop",
        "com.jetbrains.IntelliJ-IDEA-Ultimate.desktop",
        "com.jetbrains.IntelliJ-IDEA-Community.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const WEBSTORM_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "webstorm",
    display_name: "WebStorm",
    macos_app_bundles: &["WebStorm.app"],
    macos_cli: "webstorm",
    windows_install_rel: "",
    windows_clis: &["webstorm.exe", "webstorm64.exe"],
    linux_cli: "webstorm",
    linux_desktop_ids: &[
        "jetbrains-webstorm.desktop",
        "webstorm_webstorm.desktop",
        "com.jetbrains.WebStorm.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const PYCHARM_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "pycharm",
    display_name: "PyCharm",
    macos_app_bundles: &["PyCharm.app", "PyCharm Professional.app", "PyCharm CE.app"],
    macos_cli: "pycharm",
    windows_install_rel: "",
    windows_clis: &["pycharm.exe", "pycharm64.exe"],
    linux_cli: "pycharm",
    linux_desktop_ids: &[
        "jetbrains-pycharm.desktop",
        "jetbrains-pycharm-ce.desktop",
        "pycharm-professional_pycharm-professional.desktop",
        "pycharm-community_pycharm-community.desktop",
        "com.jetbrains.PyCharm-Professional.desktop",
        "com.jetbrains.PyCharm-Community.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const GOLAND_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "goland",
    display_name: "GoLand",
    macos_app_bundles: &["GoLand.app"],
    macos_cli: "goland",
    windows_install_rel: "",
    windows_clis: &["goland.exe", "goland64.exe"],
    linux_cli: "goland",
    linux_desktop_ids: &[
        "jetbrains-goland.desktop",
        "goland_goland.desktop",
        "com.jetbrains.GoLand.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const RUBYMINE_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "rubymine",
    display_name: "RubyMine",
    macos_app_bundles: &["RubyMine.app"],
    macos_cli: "rubymine",
    windows_install_rel: "",
    windows_clis: &["rubymine.exe", "rubymine64.exe"],
    linux_cli: "rubymine",
    linux_desktop_ids: &[
        "jetbrains-rubymine.desktop",
        "rubymine_rubymine.desktop",
        "com.jetbrains.RubyMine.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const PHPSTORM_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "phpstorm",
    display_name: "PhpStorm",
    macos_app_bundles: &["PhpStorm.app"],
    macos_cli: "phpstorm",
    windows_install_rel: "",
    windows_clis: &["phpstorm.exe", "phpstorm64.exe"],
    linux_cli: "phpstorm",
    linux_desktop_ids: &[
        "jetbrains-phpstorm.desktop",
        "phpstorm_phpstorm.desktop",
        "com.jetbrains.PhpStorm.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const CLION_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "clion",
    display_name: "CLion",
    macos_app_bundles: &["CLion.app"],
    macos_cli: "clion",
    windows_install_rel: "",
    windows_clis: &["clion.exe", "clion64.exe"],
    linux_cli: "clion",
    linux_desktop_ids: &[
        "jetbrains-clion.desktop",
        "clion_clion.desktop",
        "com.jetbrains.CLion.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const ANDROID_STUDIO_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "android-studio",
    display_name: "Android Studio",
    macos_app_bundles: &["Android Studio.app"],
    macos_cli: "studio",
    windows_install_rel: "",
    windows_clis: &["studio.exe", "studio64.exe"],
    linux_cli: "studio",
    linux_desktop_ids: &[
        "jetbrains-studio.desktop",
        "android-studio_android-studio.desktop",
        "com.google.AndroidStudio.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const RIDER_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "rider",
    display_name: "Rider",
    macos_app_bundles: &["Rider.app"],
    macos_cli: "rider",
    windows_install_rel: "",
    windows_clis: &["rider.exe", "rider64.exe"],
    linux_cli: "rider",
    linux_desktop_ids: &[
        "jetbrains-rider.desktop",
        "rider_rider.desktop",
        "com.jetbrains.Rider.desktop",
    ],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const MACVIM_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "macvim",
    display_name: "MacVim",
    macos_app_bundles: &["MacVim.app"],
    macos_cli: "mvim",
    windows_install_rel: "",
    windows_clis: &[],
    linux_cli: "",
    linux_desktop_ids: &[],
    cli_args: &["+{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const NEOVIDE_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "neovide",
    display_name: "Neovide",
    macos_app_bundles: &["Neovide.app"],
    macos_cli: "neovide",
    windows_install_rel: "",
    windows_clis: &["neovide.exe"],
    linux_cli: "neovide",
    linux_desktop_ids: &["neovide.desktop"],
    cli_args: &["+{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const KATE_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "kate",
    display_name: "Kate",
    macos_app_bundles: &[],
    macos_cli: "",
    windows_install_rel: "",
    windows_clis: &[],
    linux_cli: "kate",
    linux_desktop_ids: &["org.kde.kate.desktop"],
    cli_args: &["--line", "{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const GNOME_TEXT_EDITOR_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "gnome-text-editor",
    display_name: "GNOME Text Editor",
    macos_app_bundles: &[],
    macos_cli: "",
    windows_install_rel: "",
    windows_clis: &[],
    linux_cli: "gnome-text-editor",
    linux_desktop_ids: &["org.gnome.TextEditor.desktop"],
    cli_args: &["{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["{path}"],
    folder_macos_open_args: &["{path}"],
};

const NEOVIM_QT_ENTRY: EditorCatalogEntry = EditorCatalogEntry {
    id: "neovim-qt",
    display_name: "Neovim Qt",
    macos_app_bundles: &[],
    macos_cli: "",
    windows_install_rel: "",
    windows_clis: &["nvim-qt.exe"],
    linux_cli: "nvim-qt",
    linux_desktop_ids: &["nvim-qt.desktop"],
    cli_args: &["--", "+{line}", "{path}"],
    macos_open_args: &["{path}"],
    folder_cli_args: &["--", "{path}"],
    folder_macos_open_args: &["{path}"],
};

pub(crate) fn catalog() -> &'static [EditorCatalogEntry] {
    &[
        CURSOR_ENTRY,
        VSCODE_ENTRY,
        VSCODE_INSIDERS_ENTRY,
        VSCODIUM_ENTRY,
        WINDSURF_ENTRY,
        SUBLIME_ENTRY,
        BBEDIT_ENTRY,
        TEXTMATE_ENTRY,
        COTEDITOR_ENTRY,
        NOVA_ENTRY,
        ZED_ENTRY,
        XCODE_ENTRY,
        INTELLIJ_IDEA_ENTRY,
        WEBSTORM_ENTRY,
        PYCHARM_ENTRY,
        GOLAND_ENTRY,
        RUBYMINE_ENTRY,
        PHPSTORM_ENTRY,
        CLION_ENTRY,
        ANDROID_STUDIO_ENTRY,
        RIDER_ENTRY,
        MACVIM_ENTRY,
        NEOVIDE_ENTRY,
        KATE_ENTRY,
        GNOME_TEXT_EDITOR_ENTRY,
        NEOVIM_QT_ENTRY,
    ]
}
