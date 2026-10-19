"""Generate small, self-contained SVG links for the English and Chinese README."""
from html import escape
from pathlib import Path

ROOT = Path(__file__).resolve().parent
ICONS = {
    'website': '<circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3 12h18"/>',
    'language': '<path d="M3 5h12M9 3v2M12 5c0 6-4 10-9 12M5 8c1 4 4 6 8 8M14 21l4-10 4 10M15.2 18h5.6"/>',
    'start': '<path d="M5 4h14v16H5zM8 8h8M8 12h5M8 16h3"/>',
    'docs': '<path d="M12 5C8 2 3 4 3 4v15s5-2 9 1c4-3 9-1 9-1V4s-5-2-9 1v15"/>',
    'server': '<rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.01M7 17.5h.01M12 6.5h5M12 17.5h5"/>',
    'graph': '<rect x="2" y="9" width="5" height="6" rx="1"/><rect x="17" y="2" width="5" height="6" rx="1"/><rect x="17" y="16" width="5" height="6" rx="1"/><path d="M7 12h3c3 0 2-7 7-7M10 12c3 0 2 7 7 7"/>',
    'data': '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v14c0 4 16 4 16 0V5M4 12c0 4 16 4 16 0"/>',
    'code': '<path d="m8 6-6 6 6 6M16 6l6 6-6 6M14 3l-4 18"/>',
}
LABELS = {
    'start': ('Getting started', '使用指南'),
    'docs': ('Docs & help', '文档与帮助'),
    'server': ('Self-hosting', '自部署'),
    'graph': ('How it works', '工作原理'),
    'data': ('Data sharing', '数据共享'),
    'code': ('Development', '开发文档'),
}


def icon(name, x, y, scale):
    return f'<g transform="translate({x} {y}) scale({scale})" fill="none" stroke="#303030" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">{ICONS[name]}</g>'


for name in ('website', 'language'):
    content = '<rect x=".5" y=".5" width="35" height="35" rx="9" fill="#fff" stroke="#dedede"/>' + icon(name, 8, 8, 20 / 24)
    (ROOT / f'{name}.svg').write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="36" height="36" viewBox="0 0 36 36">{content}</svg>\n')

for name, labels in LABELS.items():
    for language, label in zip(('en', 'zh'), labels):
        content = '<rect x=".5" y="4.5" width="167" height="51" rx="10" fill="#fff" stroke="#dedede"/>'
        content += icon(name, 14, 20, 20 / 24)
        content += f'<text x="44" y="35" font-family="system-ui,-apple-system,Segoe UI,PingFang SC,Noto Sans SC,sans-serif" font-size="13" font-weight="500" fill="#242424">{escape(label)}</text>'
        content += '<path d="m152 27 3 3-3 3" fill="none" stroke="#777" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>'
        (ROOT / f'{name}-{language}.svg').write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="168" height="60" viewBox="0 0 168 60">{content}</svg>\n')
