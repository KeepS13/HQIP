package online.sharevps.hqip.preview;

/** Isolates APK installation grants from Godot's own file-sharing provider.
 * The manifest restricts this provider to the private updates directory.
 */
public final class UpdateFileProvider extends androidx.core.content.FileProvider { }
