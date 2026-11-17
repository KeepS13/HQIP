import unittest
from unittest.mock import patch

import upgrade_domains as deploy


class RealityControllerCompatibilityTests(unittest.TestCase):
    def test_active_reality_nodes_block_old_controller_but_initial_release_can_rollback(self):
        manifest = {'postgresSchemaVersion': 1, 'serverBillingVersion': 2,
                    'offlineAdmissionVersion': 1, 'subscriptionAllocationsVersion': 1}
        with patch.object(deploy, 'postgres_enabled', return_value=True):
            with patch.object(deploy, 'postgres_info', return_value={'vlessRealityVersion': 0}):
                deploy.require_billing_compatible(manifest)
            with patch.object(deploy, 'postgres_info', return_value={'vlessRealityVersion': 1}):
                with self.assertRaisesRegex(ValueError, 'Reality nodes'):
                    deploy.require_billing_compatible(manifest)
                deploy.require_billing_compatible({**manifest, 'vlessRealityVersion': 1})
