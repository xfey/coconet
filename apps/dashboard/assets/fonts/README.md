# Dashboard 品牌字体

`outfit-brand-400-ed2c6d023cf3.woff2` 是 Outfit Version 1.100 的固定 400 字重子集，仅包含 `Coconet` 使用的字符（含大小写 C），用于品牌标题。正文与版本号使用系统字体，不把此子集用于任意用户内容。

- 原项目：[Outfit Fonts](https://github.com/Outfitio/Outfit-Fonts)；设计者 Rodrigo Fuenzalida / fragTYPE。
- 许可：[SIL Open Font License 1.1](OFL.txt)，允许随软件分发与子集化。保留版权与许可，派生字体内部命名为 `Outfit Brand`。
- 输入：用户已安装的 Outfit 可变字体 Version 1.100，SHA-256 `bccf9dda25ddc7fb32217f386e07c72c699a902eed3abcf5665ce4377fdfa33b`。项目主页为来源参考，不将当前上游文件假定为相同字节。
- 输出：1,256 bytes，SHA-256 `ed2c6d023cf3831cdc31be31413bf6795569b5271c9c303ee68073cf4a8544db`；无可变轴，OS/2 weight 为 400。

使用 fontTools 4.60.1 / Brotli 1.1.0 生成：关闭时间戳重算，用 `instantiateVariableFont(font, {"wght": 400})` 固定字重，再以 `Subsetter.populate(text="Coconet")` 裁剪，保留 `kern` / `liga`、所有 name ID / language 和 legacy name，最后以 WOFF2 保存。内部 family / typographic family 为 `Outfit Brand`，style 为 `Regular`，full name 为 `Outfit Brand 400`，PostScript name 为 `OutfitBrand400`，unique ID 为 `Outfit Brand 400 Version 1.100`。同一输入重复生成的结果一致。再生成时先核验输入摘要，更新文件名中的输出摘要及 HTML preload、CSS URL、Server 白名单和资源测试。

字体随 Server 的 Go 嵌入资源分发，浏览器从 `/assets/fonts/` 同源加载；没有 npm 安装或运行时下载依赖。`index.html` 预加载此文件，CSS 使用 `font-display: swap` 和系统字体回退。内容摘要文件名允许一年 immutable 缓存；页面及 CSS 继续 `no-store`，升级会自然引用新字体 URL。许可与本说明也由 Server 白名单提供。
