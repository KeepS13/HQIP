package online.sharevps.hqip;

import java.util.Collections;
import java.util.Set;
import org.godotengine.godot.Godot;
import org.godotengine.godot.GodotActivity;
import org.godotengine.godot.plugin.GodotPlugin;

/** Godot owns rendering; the Android service retains VPN lifetime across UI recreation. */
public final class MainActivity extends GodotActivity {
    @Override public Set<GodotPlugin> getHostPlugins(Godot godot) {
        return Collections.singleton(new RuntimePlugin(godot));
    }
}
