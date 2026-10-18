"""Helm-rendered structure checks using only Python's standard library.

Helm validates YAML and values.schema.json. These tests inspect actual rendered
documents; the separate kind smoke test validates Kubernetes admission/runtime.
"""

import json
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


CHART = Path(__file__).resolve().parents[1]


def render(values=None, success=True):
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "values.json"
        path.write_text(json.dumps(values or {}), encoding="utf-8")
        result = subprocess.run(
            ["helm", "template", "unit", str(CHART), "--namespace", "agents-tests", "--values", str(path)],
            text=True, capture_output=True, timeout=30,
        )
    if success and result.returncode:
        raise AssertionError(result.stderr)
    if not success:
        if result.returncode == 0:
            raise AssertionError("invalid values unexpectedly rendered successfully")
        return result.stderr
    documents = {}
    for document in re.split(r"(?m)^---\s*$", result.stdout):
        kind = re.search(r"(?m)^kind:\s*(\S+)\s*$", document)
        name = re.search(r"(?m)^  name:\s*([^\n]+)$", document)
        if kind and name:
            key = (kind.group(1), name.group(1).strip('"'))
            if key in documents:
                raise AssertionError(f"duplicate Kubernetes object {key}")
            documents[key] = document
    return documents


class ChartTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if shutil.which("helm") is None:
            raise RuntimeError("Helm is required; CI uses Helm 4.3.1")
        version = re.search(r'(?m)^appVersion:\s*"?([^"\n]+)"?$', (CHART / "Chart.yaml").read_text())
        cls.app_version = version.group(1)
        cls.default = render()

    def test_three_real_components_and_single_writer(self):
        deployments = {name: text for (kind, name), text in self.default.items() if kind == "Deployment"}
        self.assertEqual(set(deployments), {f"unit-agents-{component}" for component in ("control", "edge", "console")})
        for component in ("control", "edge", "console"):
            text = deployments[f"unit-agents-{component}"]
            args = re.search(r"(?m)^          args: (\[.*\])$", text)
            self.assertEqual(json.loads(args.group(1)), [component, "--config", "/etc/agents/config.yaml"])
            self.assertIn(f'ghcr.io/bit-precise/agents-{component}:{self.app_version}', text)
            self.assertIn("  replicas: 1\n", text)
            self.assertGreaterEqual(text.count(f"app.kubernetes.io/component: {component}"), 3)
            self.assertIn("type: RollingUpdate" if component == "console" else "type: Recreate", text)
            self.assertIn(("Service", f"unit-agents-{component}"), self.default)
        for component in ("control", "edge"):
            with self.subTest(component=component):
                render({component: {"replicaCount": 2}}, success=False)
        expanded = render({"console": {"replicaCount": 3}})
        self.assertIn("  replicas: 3\n", expanded[("Deployment", "unit-agents-console")])

    def test_private_storage_subdirectory_and_retention(self):
        control = self.default[("Deployment", "unit-agents-control")]
        configuration = self.default[("ConfigMap", "unit-agents-control-config")]
        self.assertIn("mountPath: /var/lib/agents\n", control)
        self.assertNotIn("mountPath: /var/lib/agents/state", control)
        self.assertIn('stateDir: "/var/lib/agents/state"', configuration)
        self.assertIn('dsn: "/var/lib/agents/state/agents.db"', configuration)
        self.assertIn("helm.sh/resource-policy: keep", self.default[("PersistentVolumeClaim", "unit-agents-data")])
        for component in ("edge", "console"):
            self.assertNotIn("/var/lib/agents", self.default[("Deployment", f"unit-agents-{component}")])
            self.assertNotIn("database:", self.default[("ConfigMap", f"unit-agents-{component}-config")])
        existing = render({"persistence": {"existingClaim": "operator-owned"}})
        self.assertFalse(any(kind == "PersistentVolumeClaim" for kind, _ in existing))
        self.assertIn('claimName: "operator-owned"', existing[("Deployment", "unit-agents-control")])
        ephemeral = render({"persistence": {"enabled": False}})
        self.assertFalse(any(kind == "PersistentVolumeClaim" for kind, _ in ephemeral))
        self.assertIn("emptyDir: {}", ephemeral[("Deployment", "unit-agents-control")])

    def test_secret_projection_is_least_privilege(self):
        self.assertFalse(any(kind == "Secret" for kind, _ in self.default))
        control = self.default[("Deployment", "unit-agents-control")]
        edge = self.default[("Deployment", "unit-agents-edge")]
        console = self.default[("Deployment", "unit-agents-console")]
        self.assertIn("defaultMode: 0440", control)
        self.assertIn("path: admin.token", control)
        self.assertIn("path: internal.token", control)
        self.assertIn("path: internal.token", edge)
        self.assertNotIn("path: admin.token", edge)
        self.assertNotIn("database-dsn", edge)
        self.assertNotIn("projected:", console)
        self.assertNotIn("/etc/agents/secrets", console)
        render({"existingSecret": ""}, success=False)

    def test_postgres_dsn_never_enters_configmap_or_other_pods(self):
        documents = render({"database": {"type": "postgres"}, "secretKeys": {"databaseDSN": "production-pg-url"}})
        self.assertFalse(any(kind == "PersistentVolumeClaim" for kind, _ in documents))
        configuration = documents[("ConfigMap", "unit-agents-control-config")]
        self.assertIn("type: postgres", configuration)
        self.assertIn('dsnFile: "/etc/agents/secrets/database-dsn"', configuration)
        self.assertNotIn("dsn:", configuration)
        self.assertIn('key: "production-pg-url"', documents[("Deployment", "unit-agents-control")])
        for component in ("edge", "console"):
            self.assertNotIn("production-pg-url", documents[("Deployment", f"unit-agents-{component}")])
        render({"database": {"type": "postgres", "dsn": "postgres://secret@db"}}, success=False)

    def test_internal_urls_and_tls_offload(self):
        control = self.default[("ConfigMap", "unit-agents-control-config")]
        edge = self.default[("ConfigMap", "unit-agents-edge-config")]
        self.assertIn('edgeInternalURL: "http://unit-agents-edge:8080"', control)
        self.assertIn('consoleURL: "http://unit-agents-console:8080"', control)
        self.assertIn('controlURL: "http://unit-agents-control:8080"', edge)
        for component in ("control", "edge", "console"):
            text = self.default[("ConfigMap", f"unit-agents-{component}-config")]
            self.assertIn("tlsOffload: true", text)
            self.assertNotIn("developmentHTTP: true", text)
        render({"public": {"serverURL": "http://agents.example.com"}}, success=False)
        render({"public": {"serverURL": "https://127.0.0.1"}}, success=False)
        render({"public": {"serverURL": "https://agents..example.com"}}, success=False)

    def test_service_wildcards_are_one_workspace_deep(self):
        documents = render({"ingress": {"serviceHosts": ["*.workspace1.agents.example.com", "demo.workspace2.agents.example.com"]}})
        ingress = documents[("Ingress", "unit-agents")]
        blocks = dict(re.findall(r'(?ms)^    - host: "([^"]+)"\n(.*?)(?=^    - host:|\Z)', ingress))
        self.assertIn("name: unit-agents-control", blocks["agents.example.com"])
        for host in ("connect.agents.example.com", "*.workspace1.agents.example.com", "demo.workspace2.agents.example.com"):
            self.assertIn("name: unit-agents-edge", blocks[host])
            self.assertNotIn("name: unit-agents-console", blocks[host])
        self.assertIn("path: /agent", blocks["connect.agents.example.com"])
        for host in ("*.agents.example.com", "*.*.agents.example.com", "*.one.two.agents.example.com", "*.workspace.other.example"):
            with self.subTest(host=host):
                render({"ingress": {"serviceHosts": [host]}}, success=False)
        disabled = render({"ingress": {"enabled": False}})
        self.assertNotIn(("Ingress", "unit-agents"), disabled)

    def test_same_public_host_has_longest_path_edge_route(self):
        documents = render({"public": {"edgeURL": "https://agents.example.com/agent"}})
        ingress = documents[("Ingress", "unit-agents")]
        self.assertEqual(ingress.count('host: "agents.example.com"'), 1)
        self.assertIn("path: /agent", ingress)
        self.assertIn("name: unit-agents-edge", ingress)
        self.assertIn("name: unit-agents-control", ingress)

    def test_explicit_https_port_preserves_authority_but_not_ingress_host(self):
        values = {"public": {"serverURL": "https://agents.example.com:30526/",
                             "edgeURL": "https://connect.agents.example.com:30526/agent"},
                  "ingress": {"serviceHosts": ["*.demo.agents.example.com", "hello.other.agents.example.com"]},
                  "oidc": {"enabled": True, "issuer": "https://id.example.net", "clientId": "agents"}}
        documents = render(values)
        for component in ("control", "edge", "console"):
            configuration = documents[("ConfigMap", f"unit-agents-{component}-config")]
            self.assertIn('serverURL: "https://agents.example.com:30526"', configuration)
            self.assertIn('edgeURL: "https://connect.agents.example.com:30526/agent"', configuration)
        control = documents[("ConfigMap", "unit-agents-control-config")]
        self.assertIn('redirectURL: "https://agents.example.com:30526/auth/oidc/callback"', control)
        ingress = documents[("Ingress", "unit-agents")]
        hosts = re.findall(r'(?m)^    - host: "([^\"]+)"$', ingress)
        self.assertEqual(hosts, ["agents.example.com", "connect.agents.example.com",
                                 "*.demo.agents.example.com", "hello.other.agents.example.com"])
        # External Gateways still need full authorities for Host/SSO checks.
        values["ingress"]["enabled"] = False
        external = render(values)
        self.assertNotIn(("Ingress", "unit-agents"), external)
        self.assertIn(':30526/auth/oidc/callback', external[("ConfigMap", "unit-agents-control-config")])

    def test_explicit_port_bounds_and_same_hostname_routing(self):
        for port in (1, 443, 9999, 10000, 59999, 60000, 64999, 65000, 65499, 65500, 65529, 65530, 65535):
            with self.subTest(port=port):
                documents = render({"public": {"serverURL": f"https://agents.example.com:{port}",
                                                "edgeURL": f"https://agents.example.com:{port}/agent"}})
                ingress = documents[("Ingress", "unit-agents")]
                self.assertEqual(ingress.count('host: "agents.example.com"'), 1)
                self.assertIn("path: /agent", ingress)
                self.assertIn("name: unit-agents-edge", ingress)
                self.assertIn("name: unit-agents-control", ingress)
        for port in ("", "0", "00", "0443", "65536", "65599", "65600", "99999", "100000", "-1", "+443", "https", "443.0"):
            for name, suffix in (("serverURL", ""), ("edgeURL", "/agent")):
                with self.subTest(field=name, invalid_port=port):
                    render({"public": {name: f"https://agents.example.com:{port}{suffix}"}}, success=False)

    def test_service_domain_validation_remains_strict_with_public_port(self):
        for host in ("*.agents.example.com", "*.*.agents.example.com", "*.one.two.agents.example.com",
                     "*.demo.other.example", "hello.demo.agents.example.com:30526"):
            with self.subTest(host=host):
                render({"public": {"serverURL": "https://agents.example.com:30526"},
                        "ingress": {"serviceHosts": [host]}}, success=False)
        render({"public": {"serverURL": "https://127.0.0.1:30526"}}, success=False)

    def test_security_and_process_probes(self):
        for (kind, name), text in self.default.items():
            if kind != "Deployment":
                continue
            with self.subTest(component=name):
                for expected in ("runAsNonRoot: true", "runAsUser: 65532", "fsGroup: 65532", "fsGroupChangePolicy: OnRootMismatch", "readOnlyRootFilesystem: true", "allowPrivilegeEscalation: false", "- ALL", "automountServiceAccountToken: false", "startupProbe:", "readinessProbe:", "livenessProbe:", "path: /healthz", "mountPath: /tmp"):
                    self.assertIn(expected, text)
        self.assertFalse(any(kind in {"Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding"} for kind, _ in self.default))

    def test_network_policy_keeps_console_private(self):
        documents = render({"networkPolicy": {"enabled": True}, "database": {"type": "postgres"}})
        console = documents[("NetworkPolicy", "unit-agents-console")]
        edge = documents[("NetworkPolicy", "unit-agents-edge")]
        control = documents[("NetworkPolicy", "unit-agents-control")]
        self.assertNotIn("namespaceSelector:", console)
        self.assertIn("app.kubernetes.io/component: control", console)
        self.assertRegex(console, r"egress:\s*\[\]")
        self.assertIn("port: 5432", control)
        self.assertNotIn("port: 5432", edge)
        # Empty optional egress lists remain valid manifests rather than emitting
        # a stray [] in the middle of the generated YAML sequence.
        render({"database": {"type": "postgres"}, "networkPolicy": {"enabled": True, "postgresEgress": []}})

    def test_oidc_secret_is_control_only_and_redirect_is_bound(self):
        values = {"oidc": {"enabled": True, "issuer": "https://id.example.net", "clientId": "agents", "existingSecret": "corporate-oidc", "clientSecretKey": "client-secret", "bootstrapAdminSubjects": ["operator-subject"]}, "networkPolicy": {"enabled": True}}
        documents = render(values)
        control_config = documents[("ConfigMap", "unit-agents-control-config")]
        self.assertIn('redirectURL: "https://agents.example.com/auth/oidc/callback"', control_config)
        self.assertIn('clientSecretFile: "/etc/agents/secrets/client-secret"', control_config)
        self.assertIn('bootstrapAdminSubjects: ["operator-subject"]', control_config)
        self.assertIn('name: "corporate-oidc"', documents[("Deployment", "unit-agents-control")])
        for component in ("edge", "console"):
            self.assertNotIn("corporate-oidc", documents[("Deployment", f"unit-agents-{component}")])
            self.assertNotIn("oidc:", documents[("ConfigMap", f"unit-agents-{component}-config")])
        values["oidc"]["clientSecretKey"] = ""
        public_client = render(values)
        self.assertNotIn("clientSecretFile:", public_client[("ConfigMap", "unit-agents-control-config")])
        self.assertNotIn("corporate-oidc", public_client[("Deployment", "unit-agents-control")])
        render({"oidc": {"enabled": True}}, success=False)
        render({"oidc": {"enabled": True, "issuer": "http://id.example.net", "clientId": "agents"}}, success=False)
        render({"oidc": {"scopes": ["email"]}}, success=False)

    def test_existing_service_account_and_unknown_fields(self):
        documents = render({"serviceAccount": {"create": False, "name": "operator-account"}})
        self.assertFalse(any(kind == "ServiceAccount" for kind, _ in documents))
        self.assertIn("serviceAccountName: operator-account", documents[("Deployment", "unit-agents-control")])
        render({"edge": {"replicaCont": 2}}, success=False)

    def test_names_fit_kubernetes_limits(self):
        documents = render({"fullnameOverride": "a" * 48})
        self.assertTrue(all(len(name) <= 63 for _, name in documents))


if __name__ == "__main__":
    unittest.main()
