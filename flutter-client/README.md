# HQIP Preview

Independent Flutter Windows + Android client for HQIP. Uses a native Go controller,
Windows per-user named pipes, and Android platform channels. Tokens and node
credentials remain in native secure storage.

See [architecture, builds and preview updates](../docs/flutter-preview.md).
Release identity and pinned Flutter commit: [flutter-version.json](../packaging/flutter-version.json).

Run `flutter analyze` and `flutter test` with the pinned SDK. For a distributable
product use `python scripts/build_flutter.py windows` or `android` from the repo
root, so the native host, core, runtime libraries and signing metadata are included.
A standalone Flutter Windows build expects an already running preview host and
`hqip-runtime.exe` beside the UI (or the development `HQIP_PREVIEW_RUNTIME` override).

The preview has its own package ID, storage, signing identity and update channel.
It does not migrate existing users or publish updates to the production channel.
