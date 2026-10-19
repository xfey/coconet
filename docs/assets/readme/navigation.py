"""Generate small, self-contained SVG links for the English and Chinese README."""
from html import escape
from pathlib import Path

ROOT = Path(__file__).resolve().parent
ICONS = {
    'website': '<circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3 12h18"/>',
    'language': '<path d="M3 5h12M9 3v2M12 5c0 6-4 10-9 12M5 8c1 4 4 6 8 8M14 21l4-10 4 10M15.2 18h5.6"/>',
    'docs': '<path d="M12 5C8 2 3 4 3 4v15s5-2 9 1c4-3 9-1 9-1V4s-5-2-9 1v15"/>',
    'contribute': '<circle cx="9" cy="7" r="3"/><path d="M3 20v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M18 13a5 5 0 0 1 3 5v2"/>',
    'license': '<path d="M14 3H5v18h14V8l-5-5v5h5M8 14l3 3 5-6"/>',
}
LABELS = {
    'docs': ('Docs & help', '文档与帮助'),
    'contribute': ('Contributing', '贡献指南'),
    'license': ('MIT License', 'MIT 许可证'),
}
HEADER_LABELS = {
    'website': ('Website', '访问官网'),
    'language': ('阅读中文', 'Read in English'),
}


def icon(name, x, y, scale):
    return f'<g transform="translate({x} {y}) scale({scale})" fill="none" stroke="#303030" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">{ICONS[name]}</g>'


for name, labels in HEADER_LABELS.items():
    for language, label in zip(('en', 'zh'), labels):
        content = '<rect x=".5" y="4.5" width="167" height="39" rx="10" fill="#fff" stroke="#dedede"/>'
        content += icon(name, 14, 14, 20 / 24)
        content += f'<text x="44" y="29" font-family="system-ui,-apple-system,Segoe UI,PingFang SC,Noto Sans SC,sans-serif" font-size="13" font-weight="500" fill="#242424">{escape(label)}</text>'
        (ROOT / f'{name}-{language}.svg').write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="168" height="48" viewBox="0 0 168 48">{content}</svg>\n')

for name, labels in LABELS.items():
    for language, label in zip(('en', 'zh'), labels):
        content = '<rect x=".5" y="4.5" width="167" height="51" rx="10" fill="#fff" stroke="#dedede"/>'
        content += icon(name, 14, 20, 20 / 24)
        content += f'<text x="44" y="35" font-family="system-ui,-apple-system,Segoe UI,PingFang SC,Noto Sans SC,sans-serif" font-size="13" font-weight="500" fill="#242424">{escape(label)}</text>'
        content += '<path d="m152 27 3 3-3 3" fill="none" stroke="#777" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>'
        (ROOT / f'{name}-{language}.svg').write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="168" height="60" viewBox="0 0 168 60">{content}</svg>\n')
