package confine

var credentialStorePathFixture = []string{
	"~/.docker/config.json", "~/.docker/contexts", "~/.docker/daemon.json",
	"~/.config/containers/auth.json",
	"~/.aws/", "~/.azure/", "~/.config/gcloud/", "~/.kube/", "~/.oci/",
	"~/.aws/credentials", "~/.aws/config", "~/.kube/config",
	"~/.azure/msal_token_cache.json",
	"~/.config/gcloud/credentials.db",
	"~/.config/gcloud/application_default_credentials.json",
	"~/.config/gh/", "~/.config/glab-cli/", "~/.netrc", "~/.git-credentials",
	"~/.npmrc", "~/.pypirc", "~/.cargo/credentials.toml",
	"~/.m2/settings.xml", "~/.gradle/gradle.properties",
	"~/.config/op/", "~/.vault-token", "~/.terraform.d/", "~/.terraformrc",
	"~/.config/rclone/rclone.conf",
}
