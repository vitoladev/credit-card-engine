# Minimal JSON Schema checker: required, type, const, enum, pattern,
# minLength, minimum, maximum, properties, items, oneOf, $ref (local #/$defs
# only). Emits one error string per violation; an empty output means valid.
def resolve($schema; $root):
  if ($schema | has("$ref")) then
    ($schema["$ref"] | ltrimstr("#/") | split("/")) as $path
    | resolve($root | getpath($path); $root)
  else $schema end;

def typeok($v; $t):
  ($t | if type == "array" then . else [.] end) as $ts
  | ($v | type) as $vt
  | any($ts[]; . == $vt or (. == "integer" and $vt == "number" and ($v | floor == $v)));

def check($v; $schema; $root; $path):
  resolve($schema; $root) as $s
  | (if ($s | has("type")) and (typeok($v; $s.type) | not)
       then "\($path): expected \($s.type | tostring), got \($v | type)" else empty end),
    (if ($s | has("const")) and ($v != $s.const) then "\($path): must equal \($s.const | tojson)" else empty end),
    (if ($s | has("enum")) and (($s.enum | index([$v])) == null) then "\($path): \($v | tojson) not in \($s.enum | tojson)" else empty end),
    (if ($s | has("pattern")) and ($v | type == "string") and (($v | test($s.pattern)) | not) then "\($path): does not match \($s.pattern)" else empty end),
    (if ($s | has("minLength")) and ($v | type == "string") and (($v | length) < $s.minLength) then "\($path): shorter than \($s.minLength)" else empty end),
    (if ($s | has("minimum")) and ($v | type == "number") and ($v < $s.minimum) then "\($path): below \($s.minimum)" else empty end),
    (if ($s | has("maximum")) and ($v | type == "number") and ($v > $s.maximum) then "\($path): above \($s.maximum)" else empty end),
    (if ($v | type == "object") then
       (($s.required // [])[] | . as $r | select(($v | has($r)) | not) | "\($path).\($r): required"),
       (($s.properties // {}) | to_entries[] | .key as $k | .value as $sub | select($v | has($k)) | check($v[$k]; $sub; $root; "\($path).\($k)"))
     else empty end),
    (if ($v | type == "array") and ($s | has("items")) then
       ($v | to_entries[] | check(.value; $s.items; $root; "\($path)[\(.key)]"))
     else empty end),
    (if ($s | has("oneOf")) then
       ([ $s.oneOf[] | [check($v; .; $root; $path)] | length ] | map(select(. == 0)) | length) as $matches
       | if $matches != 1 then "\($path): matched \($matches) of oneOf, need exactly 1" else empty end
     else empty end);

