import { projectConnectionCommand, sharingScope } from './connection.mjs';
import { commandBlock } from './dialog.mjs';

export function renderInstallation({ content, project, apiOrigin, t, element, copyText }) {
  const guide = element('div', undefined, 'dialog-stack installation-guide');
  const steps = element('ol', undefined, 'install-steps');
  const options = { t, element, copyText, buttonClass: '' };
  function step(title, text) {
    const row = element('li', undefined, 'dialog-section');
    row.append(element('h3', t(title)));
    if (text) row.append(element('p', t(text), 'dialog-description'));
    steps.append(row); return row;
  }
  const install = step('install_step');
  install.append(commandBlock('npm install -g coconet', options));
  const connect = step('install_connect_step', 'install_connect_help');
  if (project) connect.append(element('p', t('install_target', { name: project.name }), 'muted'));
  connect.append(commandBlock(projectConnectionCommand(project?.project_id, apiOrigin), options));
  step('install_agent_step', 'install_agent_help');
  const scope = sharingScope({ t, element });
  scope.querySelector('.disclosure-body').prepend(element('p', t('connect_folder_explanation'), 'dialog-description'));
  guide.append(steps, scope);
  content.replaceChildren(guide);
}
