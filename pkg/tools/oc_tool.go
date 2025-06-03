package tools

// import (
// 	"context"
// 	"fmt"
// 	"os"
// 	"os/exec"

// 	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
// )

func init() {
	RegisterTool(&OcClient{})
}

type OcClient struct{}

func (t *OcClient) Name() string {
	return "oc"
}

func (t *OcClient) Description() string {
	return `Executes an oc (OpenShift Client) command against the user's OpenShift or Kubernetes cluster. Use this tool for OpenShift-specific operations, user management, cluster operator status, operator status, or when you need to query or modify the state of the user's OpenShift/Kubernetes cluster.

KEYWORDS: openshift, oc, user, users, project, projects, role, roles, get users, check openshift user, manage openshift users, whoami, current user, which user am i, logged in user, show user, display user, identity, login, oc whoami, clusteroperator, cluster operator, cluster status, cluster health, get co, oc get co, check clusteroperator, check cluster status, check cluster health, operator, operators, operator status, list operators, get operators, oc get operators, oc get operator, oc describe operator, csv, clusterserviceversion, oc get csv, operator version, operator install, operator upgrade

IMPORTANT: Interactive commands are not supported in this environment. This includes:
- oc exec with -it flag (use non-interactive exec instead)
- oc edit (use oc get -o yaml, oc patch, or oc apply instead)
- oc port-forward (use alternative methods like NodePort or LoadBalancer)

For interactive operations, please use these non-interactive alternatives:
- Instead of 'oc edit', use 'oc get -o yaml' to view, 'oc patch' for targeted changes, or 'oc apply' to apply full changes
- Instead of 'oc exec -it', use 'oc exec' with a specific command
- Instead of 'oc port-forward', use service types like NodePort or LoadBalancer

Examples:
- To check OpenShift users: oc get users
- To get current user: oc whoami
- To check which OpenShift user you are: oc whoami
- To answer 'which openshift user am i': oc whoami
- To answer 'who am i logged in as': oc whoami
- To list projects: oc get projects
- To get user roles: oc describe user <username>
- To check status of ClusterOperator: oc get co -o wide
- To check cluster health: oc get co
- To check a specific operator: oc get co authentication
- To list all operators: oc get co
- To get operator status: oc get co
- To describe an operator: oc describe co <operator-name>
- To list installed operators (ClusterServiceVersions): oc get csv
- To check operator versions: oc get csv
- To check operator install/upgrade status: oc get csv`
}
