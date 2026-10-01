# Native app evidence

Native desktop and mobile GUI applications cannot be grounded through host-level desktop screen recording tools (such as macOS `screencapture`, Linux `scrot`, or `xwd`), because external capture requires ambient OS permissions, captures desktop clutter, and depends on window focus.

Instead, native apps provide grounded visual receipts through **in-process headless self-snapshotting** triggered by the standard environment variable `APP_SNAPSHOT`.

---

## Universal snapshot contract

* **Trigger**: Detect `APP_SNAPSHOT` in process environment arguments or flags.
* **Optional controls**:
  * `APP_SNAPSHOT_SCALE`: Display scale factor (default: `2.0` for retina quality).
  * `APP_SNAPSHOT_VIEW`: Optional view, route, or screen identifier.
* **Behavior**:
  1. Boot the application in headless mode (do not activate foreground window).
  2. Instantiate the target view hierarchy, optionally seeding test/mock state.
  3. Render the view offscreen to a PNG bitmap.
  4. Write the PNG bytes to the path specified in `APP_SNAPSHOT`.
  5. Terminate the process immediately with exit code 0.

---

## Framework implementations

### 1. Swift & SwiftUI (macOS 13+)

In your `App` or `NSApplicationDelegate`:

```swift
final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        if let out = ProcessInfo.processInfo.environment["APP_SNAPSHOT"] {
            Task { @MainActor in
                let scale = Double(ProcessInfo.processInfo.environment["APP_SNAPSHOT_SCALE"] ?? "2.0") ?? 2.0
                let view = ContentView()
                    .frame(width: 1024, height: 768)

                let renderer = ImageRenderer(content: view)
                renderer.scale = scale
                if let img = renderer.nsImage,
                   let tiff = img.tiffRepresentation,
                   let png = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]) {
                    try? png.write(to: URL(fileURLWithPath: out))
                }
                NSApp.terminate(nil)
            }
            return
        }
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
    }
}
```

### 2. AppKit (macOS NSView)

For pure `NSView` hierarchies:

```swift
if let out = ProcessInfo.processInfo.environment["APP_SNAPSHOT"] {
    let targetView = MyRootView(frame: NSRect(x: 0, y: 0, width: 1024, height: 768))
    if let rep = targetView.bitmapImageRepForCachingDisplay(in: targetView.bounds) {
        targetView.cacheDisplay(in: targetView.bounds, to: rep)
        if let png = rep.representation(using: .png, properties: [:]) {
            try? png.write(to: URL(fileURLWithPath: out))
        }
    }
    exit(0)
}
```

### 3. Flutter

```dart
void main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final snapshotPath = Platform.environment['APP_SNAPSHOT'];
  if (snapshotPath != null) {
    final boundary = GlobalKey();
    runApp(RepaintBoundary(key: boundary, child: const MyApp()));
    WidgetsBinding.instance.addPostFrameCallback((_) async {
      final image = await boundary.toImage(pixelRatio: 2.0);
      final byteData = await image.toByteData(format: ImageByteFormat.png);
      if (byteData != null) {
        await File(snapshotPath).writeAsBytes(byteData.buffer.asUint8List());
      }
      exit(0);
    });
    return;
  }
  runApp(const MyApp());
}
```

### 4. Qt / C++

```cpp
int main(int argc, char *argv[]) {
    QApplication app(argc, argv);
    const char *snapshotPath = qgetenv("APP_SNAPSHOT");
    if (snapshotPath && *snapshotPath) {
        MainWindow window;
        window.resize(1024, 768);
        QPixmap pixmap = window.grab();
        pixmap.save(QString::fromUtf8(snapshotPath), "PNG");
        return 0;
    }
    MainWindow window;
    window.show();
    return app.exec();
}
```

---

## Execution options

1. **One-shot via `command`**:
   ```json
   {
     "name": "command",
     "args": {
       "command": "build/MyApp.app/Contents/MacOS/MyApp",
       "snapshot_capture": {
         "caption": "Main window layout"
       }
     }
   }
   ```
   The host injects `APP_SNAPSHOT`, runs the command, verifies zero exit, screens the image, and attaches visual perception immediately.

2. **Standalone run + `view_image`**:
   ```bash
   APP_SNAPSHOT=build/preview.png build/MyApp.app/Contents/MacOS/MyApp
   ```
   followed by:
   ```json
   {
     "name": "view_image",
     "args": {
       "path": "build/preview.png"
     }
   }
   ```
