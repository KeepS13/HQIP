shader_type canvas_item;
// Original radial-gradient, clipped to the inside of the one-pixel card border.
uniform vec4 base_color : source_color;
uniform vec4 glow_color : source_color;
uniform vec2 center = vec2(0.9, 0.0);
uniform vec2 dimensions = vec2(700.0, 174.0);
uniform float radius = 11.0;
void fragment() {
	vec2 half_size = dimensions * 0.5;
	vec2 q = abs(UV * dimensions - half_size) - half_size + radius;
	float edge = length(max(q, vec2(0.0))) + min(max(q.x, q.y), 0.0) - radius;
	vec2 extent = max(center, vec2(1.0) - center) * 1.41421356;
	float fade = clamp(1.0 - length((UV - center) / extent) / 0.7, 0.0, 1.0);
	COLOR = vec4(mix(base_color.rgb, glow_color.rgb, glow_color.a * fade), 1.0 - smoothstep(-0.5, 0.5, edge));
}
