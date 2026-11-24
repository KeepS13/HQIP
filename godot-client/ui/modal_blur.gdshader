shader_type canvas_item;
// A modal-only screen sample. The main UI has no perpetual blur or animation.
uniform sampler2D screen_texture : hint_screen_texture, repeat_disable, filter_linear_mipmap;
void fragment() {
	vec3 blurred = textureLod(screen_texture, SCREEN_UV, 3.0).rgb;
	COLOR = vec4(mix(blurred, vec3(0.02745, 0.04314, 0.07451), 0.72), 1.0);
}
