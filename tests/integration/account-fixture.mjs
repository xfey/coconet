import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

// Candidate sync tests own a loopback server and operator-issued test keys.
// Browser/device authorization has its own end-to-end fixture.
export async function seedFixtureLogin(home, serverURL, key) {
  const origin = new URL(serverURL);
  if (origin.protocol !== 'http:' || origin.hostname !== '127.0.0.1' || !origin.port || origin.username || origin.password) {
    throw new Error('Account fixture requires its own loopback server');
  }
  const get = async (path, headers = {}) => {
    const response = await fetch(serverURL + path, { headers, redirect: 'error' });
    if (!response.ok) throw new Error('Isolated account fixture request failed');
    return response.json();
  };
  const deployment = await get('/v1/deployment?challenge=fixture_account_setup_1234');
  const account = await get('/v1/account', { Authorization: 'Bearer ' + key });
  if (!deployment.deployment_id || !deployment.public_key_sha256 || !account.account_id) {
    throw new Error('Isolated account fixture identity is incomplete');
  }
  await mkdir(home, { recursive: true, mode: 0o700 });
  await writeFile(join(home, 'credentials.json'), JSON.stringify({
    schema_version: 2,
    credentials: [{
      schema_version: 2, deployment_id: deployment.deployment_id,
      server_url: serverURL, server_public_key_sha256: deployment.public_key_sha256,
      account, access_token: key, token_storage: 'file',
    }],
  }) + '\n', { mode: 0o600, flag: 'wx' });
}
