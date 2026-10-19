const scenes = ['graph', 'query', 'fork'];
const agents = {
  codex:{ label:'Codex', file:'openai.svg' },
  claude:{ label:'Claude Code', file:'claude.svg' },
  cursor:{ label:'Cursor CLI', file:'cursor.svg' },
};
const copy = {
  zh: {
    preview:'README 宣传图样张', image:'查看当前图片', animation:'查看动图', language:'语言',
    project:'周末徒步报名页', projectLabel:'项目', supports:'支持', carousel:'宣传图轮播', pause:'暂停轮播', play:'播放轮播', example:'示例对话',
    scenes:['工作图', '查询进展', '接力会话'],
    headings:[['共享会话，', '接力工作。'], ['同事的进展，', '一问就知道。'], ['接着会话，', '继续工作。']],
    questions:['', '报名页面现在做到哪了？', '接着 Alex 的会话，报名成功后加个日历提醒。'],
    answers:['', 'Maya 做好了手机适配，Kai 已验证完整报名流程。', '已准备好，可以在新的 Claude Code 会话中接着 Alex 的进度继续。'],
    handoffCaption:'支持跨 Agent 接力 · Codex → Claude Code',
    descriptions:[
      '周末徒步报名页：确定页面内容后，一路展示活动时间地点并适配手机，一路制作报名表单与成功页；汇合测试报名流程，再防止重复报名。',
      '询问报名页面的进展，工作图突出 Maya 已完成的手机适配和 Kai 已验证的报名流程。',
      '从 Alex 制作报名成功页的 Codex 会话分出你的新会话，在 Claude Code 中继续添加日历提醒，保留原有工作分支。',
    ],
    nodes:['确定报名页内容', '展示时间与地点', '制作报名表单', '适配手机浏览', '制作报名成功页', '测试报名流程', '防止重复报名', '添加日历提醒'],
    you:'你',
  },
  en: {
    preview:'README image preview', image:'View this image', animation:'View animation', language:'Language',
    project:'Weekend hike sign-up page', projectLabel:'Project', supports:'Supports', carousel:'Image carousel', pause:'Pause', play:'Play', example:'Example conversation',
    scenes:['Work graph', 'Team progress', 'Session handoff'],
    headings:[['Share sessions.', 'Build together.'], ['Team progress,', 'just an ask away.'], ['Pick up a session.', 'Keep building.']],
    questions:['', 'How’s the sign-up page coming along?', 'Pick up Alex’s session. Add a calendar reminder after sign-up.'],
    answers:['', 'Maya finished the mobile layout. Kai tested the full sign-up flow.', 'Done. Continue in Claude Code with Alex’s session context.'],
    handoffCaption:'Cross-agent handoff · Codex → Claude Code',
    descriptions:[
      'A weekend hike sign-up page: plan the content, build activity details and the form in parallel, add mobile layout and a success page, test the full flow, then prevent duplicate sign-ups.',
      'Ask how the sign-up page is progressing. Highlight Maya’s completed mobile layout and Kai’s full sign-up test.',
      'Branch from Alex’s Codex session for the success page, then add a calendar reminder in your own Claude Code session. Keep the original work branches.',
    ],
    nodes:['Plan the sign-up page', 'Add hike details', 'Build sign-up form', 'Adapt layout for mobile', 'Build confirmation page', 'Test full sign-up flow', 'Prevent duplicate sign-ups', 'Add calendar reminder'],
    you:'You',
  },
};
const nodes = [
  { id:'plan', x:0, y:144, author:'Alex', agent:'codex' },
  { id:'activity', x:232, y:32, author:'Maya', agent:'claude' },
  { id:'form', x:232, y:256, author:'Kai', agent:'cursor' },
  { id:'mobile', x:464, y:32, author:'Maya', agent:'claude' },
  { id:'success', x:464, y:256, author:'Alex', agent:'codex' },
  { id:'walkthrough', x:696, y:144, author:'Kai', agent:'cursor' },
  { id:'duplicates', x:696, y:352, author:'Alex', agent:'codex', current:true },
  { id:'handoff', x:464, y:400, agent:'claude', handoff:true },
];
const edges = [
  ['plan','activity','M190 202C211 202 211 90 232 90'],
  ['plan','form','M190 202C211 202 211 314 232 314'],
  ['activity','mobile','M422 90H464'],
  ['form','success','M422 314H464'],
  ['mobile','walkthrough','M654 90C675 90 675 202 696 202'],
  ['success','walkthrough','M654 314C675 314 675 202 696 202'],
  ['walkthrough','duplicates','M791 260V352'],
  ['success','handoff','M559 372V400'],
];
const nodeRoot = document.querySelector('#nodes');
for (const node of nodes) {
  const card = document.createElement('div');
  card.className = 'work-node' + (node.current ? ' current' : '') + (node.handoff ? ' handoff' : '');
  card.dataset.node = node.id;
  card.style.left = node.x + 'px'; card.style.top = node.y + 'px';
  const title = document.createElement('p');
  const meta = document.createElement('div'); meta.className = 'node-meta';
  const author = document.createElement('span'); author.className = 'node-author';
  const mark = document.createElement('span'); mark.className = 'agent-mark';
  mark.dataset.agent = node.agent; mark.title = agents[node.agent].label;
  const icon = document.createElement('img');
  icon.src = '../agent-marks/' + agents[node.agent].file;
  icon.alt = agents[node.agent].label; icon.width = 18; icon.height = 18;
  mark.append(icon); meta.append(author,mark); card.append(title,meta);
  nodeRoot.append(card);
}
for (const [from, to, path] of edges) {
  const edge = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  edge.setAttribute('d', path); edge.dataset.from = from; edge.dataset.to = to;
  edge.setAttribute('class', 'edge' + (to === 'handoff' ? ' handoff' : ''));
  document.querySelector('#edges').append(edge);
}

const selector = document.querySelector('#language');
const poster = document.querySelector('.poster');
const frame = document.querySelector('.preview-frame');
const controls = document.querySelector('.carousel-controls');
const buttons = [...document.querySelectorAll('.scene-controls button')];
const motion = matchMedia('(prefers-reduced-motion: reduce)');
const parameters = new URLSearchParams(location.search);
let language = parameters.get('lang') === 'en' ? 'en' : 'zh';
let scene = scenes.includes(parameters.get('scene')) ? parameters.get('scene') : 'graph';
let playing = !motion.matches && parameters.get('autoplay') !== '0';
let hovering = false, focused = false, timer;

function render(animate = false) {
  const strings = copy[language], index = scenes.indexOf(scene);
  document.documentElement.lang = language === 'zh' ? 'zh-CN' : 'en';
  document.title = 'Coconet · ' + strings.preview;
  selector.value = language;
  for (const node of document.querySelectorAll('[data-copy]')) {
    if (strings[node.dataset.copy] !== undefined) node.textContent = strings[node.dataset.copy];
  }
  document.querySelectorAll('#headline span').forEach((line, i) => { line.textContent = strings.headings[index][i]; });
  nodes.forEach((node, i) => {
    const card = nodeRoot.children[i];
    card.hidden = Boolean(node.handoff && scene !== 'fork');
    card.querySelector('p').textContent = strings.nodes[i];
    card.querySelector('.node-author').textContent = node.author || strings.you;
  });
  document.querySelector('.edge.handoff').toggleAttribute('hidden', scene !== 'fork');
  poster.dataset.scene = scene;
  document.querySelector('.workflow').setAttribute('aria-label', strings.descriptions[index]);
  const note = document.querySelector('.conversation-note');
  note.hidden = scene === 'graph'; note.setAttribute('aria-label', strings.example);
  note.querySelector('.question').textContent = strings.questions[index];
  note.querySelector('.answer').textContent = strings.answers[index];
  const caption = note.querySelector('.handoff-caption');
  caption.hidden = scene !== 'fork'; caption.textContent = strings.handoffCaption;
  buttons.forEach((button, i) => {
    button.textContent = strings.scenes[i];
    button.setAttribute('aria-pressed', String(scenes[i] === scene));
  });
  controls.setAttribute('aria-label', strings.carousel);
  document.querySelector('#image-link').href = (scene === 'graph' ? 'promo-' : 'promo-' + scene + '-') + language + '.png';
  document.querySelector('#animation-link').href = 'promo-' + language + '.gif';
  const announcement = document.querySelector('#slide-announcement');
  announcement.setAttribute('aria-live', playing ? 'off' : 'polite');
  announcement.textContent = (index + 1) + ' / 3 · ' + strings.scenes[index];
  document.querySelector('#playback-label').textContent = strings[playing ? 'pause' : 'play'];
  document.querySelector('.playback-icon').innerHTML = playing
    ? '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M5 3v10M11 3v10"/></svg>'
    : '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><path d="m5 3 8 5-8 5Z"/></svg>';
  if (animate && !motion.matches) {
    poster.classList.remove('changing'); void poster.offsetWidth; poster.classList.add('changing');
  }
}
function schedule() {
  clearTimeout(timer);
  if (!playing || hovering || focused || document.hidden) return;
  timer = setTimeout(() => { scene = scenes[(scenes.indexOf(scene) + 1) % scenes.length]; render(true); schedule(); }, 6500);
}
function choose(next) { playing = false; scene = next; render(true); schedule(); }
buttons.forEach(button => button.addEventListener('click', () => choose(button.dataset.scene)));
controls.addEventListener('keydown', event => {
  if (!event.target.matches('[data-scene]') || !['ArrowLeft','ArrowRight','Home','End'].includes(event.key)) return;
  event.preventDefault();
  const index = scenes.indexOf(scene);
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? 2 : (index + (event.key === 'ArrowRight' ? 1 : 2)) % 3;
  choose(scenes[next]); buttons[next].focus();
});
document.querySelector('#playback').addEventListener('click', () => { playing = !playing; if (playing) focused = false; render(); schedule(); });
selector.addEventListener('change', () => { language = selector.value; render(); schedule(); });
frame.addEventListener('pointerenter', () => { hovering = true; schedule(); });
frame.addEventListener('pointerleave', () => { hovering = false; schedule(); });
document.addEventListener('focusin', () => { focused = true; schedule(); });
document.addEventListener('focusout', () => { focused = false; schedule(); });
document.addEventListener('visibilitychange', schedule);
motion.addEventListener('change', () => { if (motion.matches) playing = false; render(); schedule(); });
function fitPoster() { frame.style.setProperty('--poster-scale', frame.clientWidth / 1440); }
new ResizeObserver(fitPoster).observe(frame);
fitPoster(); render(); schedule();
