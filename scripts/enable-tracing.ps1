# Apply guarded source edits while accepting prior gofmt whitespace differences.
$ErrorActionPreference = "Stop"
$utf8WithoutBom = [System.Text.UTF8Encoding]::new($false)
$rules = Get-Content -Raw -LiteralPath "scripts\tracing-edits.json" | ConvertFrom-Json
$changes = @{}
foreach ($rule in $rules) {
    $path = Join-Path (Get-Location).Path $rule.path
    $text = if ($changes.ContainsKey($path)) { $changes[$path] } else { [System.IO.File]::ReadAllText($path) }
    $indices = [System.Collections.Generic.List[int]]::new()
    $builder = [System.Text.StringBuilder]::new()
    for ($i = 0; $i -lt $text.Length; $i++) {
        if (-not [char]::IsWhiteSpace($text[$i])) {
            $indices.Add($i)
            [void]$builder.Append($text[$i])
        }
    }
    $normalized = $builder.ToString()
    $old = $rule.old -replace '\s', ''
    $new = $rule.new -replace '\s', ''
    if ($normalized.Contains($new)) { $changes[$path] = $text; continue }
    $start = $normalized.IndexOf($old, [System.StringComparison]::Ordinal)
    if ($start -lt 0 -or $normalized.IndexOf($old, $start + 1, [System.StringComparison]::Ordinal) -ge 0) {
        throw "Expected source fragment is missing or ambiguous: $($rule.path). No files have been written."
    }
    $first = $indices[$start]
    $last = $indices[$start + $old.Length - 1]
    $changes[$path] = $text.Substring(0, $first) + $rule.new + $text.Substring($last + 1)
}
# All anchors must validate before any file is written. Re-running is idempotent.
foreach ($path in $changes.Keys) {
    [System.IO.File]::WriteAllText($path, $changes[$path], $utf8WithoutBom)
}
Write-Output "Tracing source hooks applied. Run gofmt, resolve pinned modules and validate before committing."
