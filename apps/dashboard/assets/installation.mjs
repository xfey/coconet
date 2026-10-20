import { projectConnectionCommand, sharingScope } from './connection.mjs';
import { commandBlock, disclosure } from './dialog.mjs';

// Directory names are examples, not repository identities. Only suggest a name
// when every reported folder agrees, including folders on later pages.
export async function loadProjectFolderExample(projectID, request, signal) {
  if (!projectID) return '';
  let name = '', cursor = '';
  do {
    const page = await request('/projects/' + encodeURIComponent(projectID) + '/workspaces?limit=200' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), { signal });
    for (const folder of page.folders) {
      const next = folder.path.replace(/\/+$/, '').split('/').at(-1);
      if (!next || name && next !== name) return '';
      name = next;
    }
    cursor = page.next_cursor || '';
  } while (cursor);
  return name ? name + '/' : '';
}

export function renderInstallation({ content, project, folderExample = '', apiOrigin, t, element, copyText }) {
  const guide = element('div', undefined, 'dialog-stack installation-guide');
  const options = { t, element, copyText, buttonClass: '' };
  const connect = element('section', undefined, 'dialog-section');
  const intro = element('p', undefined, 'dialog-description');
  const updateExample = folder => { intro.textContent = folder ? t('connect_guide_intro_example', { folder }) : t('connect_guide_intro'); };
  updateExample(folderExample);
  connect.append(intro);
  connect.append(commandBlock(projectConnectionCommand(project?.project_id, apiOrigin), options));
  connect.append(element('p', t('connect_guide_next'), 'dialog-description'));
  const install = disclosure(t('connect_first_use'), { element, key: 'first-use' });
  install.body.append(element('p', t('connect_install_help'), 'dialog-description'),
    commandBlock('npm install -g coconet', options),
    element('p', t('connect_hook_help'), 'dialog-description'));
  const scope = sharingScope({ t, element });
  scope.querySelector('.disclosure-body').prepend(element('p', t('connect_folder_explanation'), 'dialog-description'));
  guide.append(connect, install.details, scope);
  for (const previous of content.querySelectorAll('details[data-disclosure]')) {
    const current = [...guide.querySelectorAll('details[data-disclosure]')].find(node => node.dataset.disclosure === previous.dataset.disclosure);
    if (current) current.open = previous.open;
  }
  content.replaceChildren(guide);
  return { updateExample };
}
