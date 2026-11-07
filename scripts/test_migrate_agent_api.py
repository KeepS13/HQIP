from pathlib import Path
from unittest.mock import patch
import json, tempfile, unittest
import migrate_agent_api as migration

class AgentDomainRollbackTests(unittest.TestCase):
 def test_restores_exact_previous_official_origin_without_rewinding_ledger(self):
  for old in migration.OLD_ORIGINS:
   with self.subTest(old=old),tempfile.TemporaryDirectory() as folder:
    backup=Path('/root/backups/domain-migration')
    config=Path(folder);cfg={'controlUrl':migration.NEW,'token':'unchanged'}
    info=json.dumps({'node':'node1','old':old,'new':migration.NEW})
    with patch.object(Path,'read_text',return_value=info),patch.object(Path,'is_symlink',return_value=False),patch.object(migration.node_ops,'owned_paths',return_value=(config,None,cfg)),patch.object(migration.node_ops,'command') as command:
     migration.rollback(backup)
    self.assertEqual(json.loads((config/'agent.json').read_text()),{'controlUrl':old,'token':'unchanged'})
    command.assert_called_once_with('systemctl','restart','hqip-agent-node1.service')
 def test_rejects_backup_with_foreign_control_origin(self):
  with tempfile.TemporaryDirectory() as folder:
   config=Path(folder);cfg={'controlUrl':migration.NEW}
   with patch.object(Path,'read_text',return_value=json.dumps({'node':'node1','old':'https://other.example','new':migration.NEW})),patch.object(Path,'is_symlink',return_value=False),patch.object(migration.node_ops,'owned_paths',return_value=(config,None,cfg)),patch.object(migration.node_ops,'command') as command:
    with self.assertRaises(ValueError):migration.rollback(Path('/root/backups/domain-migration'))
    command.assert_not_called()
    self.assertFalse((config/'agent.json').exists())

if __name__=='__main__':unittest.main()
