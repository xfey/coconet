import { projectConnectionCommand, sharingScope } from './connection.mjs';
import { commandBlock, disclosure } from './dialog.mjs';

export function renderInstallation({ content, project, apiOrigin, t, element, copyText }) {
  const guide = element('div', undefined, 'dialog-stack installation-guide');
  const options = { t, element, copyText, buttonClass: '' };
  const connect = element('section', undefined, 'dialog-section');
  connect.append(element('p', t('connect_guide_intro'), 'dialog-description'));
  if (project) connect.append(element('p', t('connect_target', { name: project.name }), 'dialog-description'));
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
}
