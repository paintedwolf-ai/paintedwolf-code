# Paged views

The controller retains bounded row detail and tracks outstanding viewport requests independently of the mounted presenter. A source adapter supplies typed frames and their retained cost; the presenter supplies interaction and pixel geometry.

A frame is installed under one view, intent revision and projection revision. Intent changes fence outstanding requests and clear incompatible detail. Projection updates retain displayed frames until replacement rows arrive; the host validates fingerprints before frames can be reused under another revision. Canceling a waiter preserves requests with other waiters. Detaching a surface aborts viewport requests and preserves cached frames and its host view.

The fixed-height geometry adapter maps logical rows onto a bounded physical scroll segment. The source adapter retains anchors across segment recentering and projection changes. CodeMirror keeps its own measured-height adapter.
