package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// A sandbox is a user's own namespace for agent sessions (Claude Code under
// gVisor). The platform makes it from the user's entry, a
// ResourceSetInputProvider in flux-system (the maxcloud repo's
// platform/sandbox/sessions/users.yaml), and a session is a SandboxClaim in
// it: one pod, named after the claim.
const (
	// SandboxLabel marks a sandbox entry, and the namespace made from it.
	SandboxLabel = "run.gronblom.dev/sandbox"
	// SandboxTokenSecret holds the user's Claude subscription token, which
	// sessions read as SandboxTokenKey.
	SandboxTokenSecret = "claude-code"
	SandboxTokenKey    = "CLAUDE_CODE_OAUTH_TOKEN"
	// SandboxContainer is the session pod's container.
	SandboxContainer = "claude"
	// SandboxWorkDir is where a session works, and its home.
	SandboxWorkDir = "/work"

	// sandboxPool is the pool sessions are claimed on (empty: every claim
	// starts a new pod).
	sandboxPool = "claude-code"
)

var (
	gvrSandboxClaim = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxclaims"}
	gvrSandboxPool  = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxwarmpools"}
)

// The same rule as the platform's admission policy: the name goes into a
// namespace name.
var sandboxNameRe = regexp.MustCompile(`^[a-z]([a-z0-9]|-[a-z0-9])*$`)

// SandboxNamespace is the namespace of user's sandbox.
func SandboxNamespace(user string) string { return "sbx-" + user }

func sandboxEntryName(user string) string { return "sandbox-" + user }

// CheckSandboxUser: user can have a sandbox.
func CheckSandboxUser(user string) error {
	if !sandboxNameRe.MatchString(user) || len(user) > 40 {
		return fmt.Errorf("user %q can't have a sandbox: its namespace needs a name of lowercase letters, digits and single dashes, starting with a letter, at most 40 characters", user)
	}
	return nil
}

// CheckSessionName: name can be a session's (it becomes a pod's name).
func CheckSessionName(name string) error {
	if !sandboxNameRe.MatchString(name) || len(name) > 50 {
		return fmt.Errorf("session name %q: use lowercase letters, digits and single dashes, starting with a letter, at most 50 characters", name)
	}
	return nil
}

// SandboxUser is the Authelia username the cluster sees this client as:
// whose sandbox its commands are about.
func (c *Client) SandboxUser(ctx context.Context) (string, error) {
	who, err := c.Whoami(ctx)
	if err != nil {
		return "", err
	}
	user, ok := strings.CutPrefix(who, AutheliaPrefix)
	if !ok {
		return "", fmt.Errorf("the cluster sees you as %s, not as a user logged in with mcloud login: log in, or name whose sandbox with --user", who)
	}
	return user, CheckSandboxUser(user)
}

func sandboxEntry(user string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1",
		"kind":       "ResourceSetInputProvider",
		"metadata": map[string]any{
			"name":      sandboxEntryName(user),
			"namespace": FluxNamespace,
			"labels": map[string]any{
				SandboxLabel:                   "true",
				"app.kubernetes.io/managed-by": FieldManager,
			},
		},
		"spec": map[string]any{
			"type":          "Static",
			"defaultValues": map[string]any{"user": user},
		},
	}}
}

// HasSandbox: user has a sandbox entry.
func (c *Client) HasSandbox(ctx context.Context, user string) (bool, error) {
	o, err := c.Dyn.Resource(gvrProvider).Namespace(FluxNamespace).Get(ctx, sandboxEntryName(user), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if o.GetLabels()[SandboxLabel] != "true" {
		return false, fmt.Errorf("%s/%s exists and isn't a sandbox entry: an admin has to remove it", FluxNamespace, o.GetName())
	}
	return true, nil
}

// EnsureSandbox makes user's sandbox entry if there's none, and says
// whether it did. The platform then makes the namespace (SandboxReady).
func (c *Client) EnsureSandbox(ctx context.Context, user string) (bool, error) {
	if err := CheckSandboxUser(user); err != nil {
		return false, err
	}
	has, err := c.HasSandbox(ctx, user)
	if err != nil || has {
		return false, err
	}
	_, err = c.Dyn.Resource(gvrProvider).Namespace(FluxNamespace).Create(ctx, sandboxEntry(user), metav1.CreateOptions{FieldManager: FieldManager})
	if apierrors.IsAlreadyExists(err) {
		return false, nil
	}
	return err == nil, err
}

// RemoveSandbox deletes user's entry: the platform then deletes the
// namespace, its sessions and the token.
func (c *Client) RemoveSandbox(ctx context.Context, user string) error {
	has, err := c.HasSandbox(ctx, user)
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf("%s has no sandbox", user)
	}
	return c.Dyn.Resource(gvrProvider).Namespace(FluxNamespace).Delete(ctx, sandboxEntryName(user), metav1.DeleteOptions{})
}

// SandboxReady: user's namespace is there and this client may use it (the
// pool sessions are claimed on can be read). False while the platform is
// still making it.
func (c *Client) SandboxReady(ctx context.Context, user string) (bool, error) {
	_, err := c.Dyn.Resource(gvrSandboxPool).Namespace(SandboxNamespace(user)).Get(ctx, sandboxPool, metav1.GetOptions{})
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
		return false, nil
	}
	return err == nil, err
}

// WaitSandbox waits until user's sandbox is ready (ctx bounds it).
func (c *Client) WaitSandbox(ctx context.Context, user string) error {
	for {
		ready, err := c.SandboxReady(ctx, user)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("the sandbox of %s isn't ready (namespace %s): the platform makes it within a minute of its entry, so something is wrong there", user, SandboxNamespace(user))
		}
	}
}

// HasSandboxToken: user has set a Claude token.
func (c *Client) HasSandboxToken(ctx context.Context, user string) (bool, error) {
	s, err := c.Core.CoreV1().Secrets(SandboxNamespace(user)).Get(ctx, SandboxTokenSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(s.Data[SandboxTokenKey]) > 0, nil
}

// SetSandboxToken stores user's Claude token: the one Secret a sandbox
// namespace holds. New sessions read it; running ones keep the token they
// started with.
func (c *Client) SetSandboxToken(ctx context.Context, user, token string) error {
	secrets := c.Core.CoreV1().Secrets(SandboxNamespace(user))
	data := map[string][]byte{SandboxTokenKey: []byte(token)}
	s, err := secrets.Get(ctx, SandboxTokenSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: SandboxTokenSecret, Labels: map[string]string{"app.kubernetes.io/managed-by": FieldManager}},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, metav1.CreateOptions{FieldManager: FieldManager})
		return err
	}
	if err != nil {
		return err
	}
	s.Data, s.StringData = data, nil
	_, err = secrets.Update(ctx, s, metav1.UpdateOptions{FieldManager: FieldManager})
	return err
}

// UnsetSandboxToken removes user's token, and says whether there was one.
func (c *Client) UnsetSandboxToken(ctx context.Context, user string) (bool, error) {
	err := c.Core.CoreV1().Secrets(SandboxNamespace(user)).Delete(ctx, SandboxTokenSecret, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// Session is one agent session: a SandboxClaim, and the pod of the same
// name it makes.
type Session struct {
	Name    string
	Created time.Time
	// Ready: the pod is up. If not, Reason and Message say why.
	Ready   bool
	Reason  string
	Message string
	// Until is when the session ends by itself (zero: never).
	Until time.Time
}

func sessionOf(o *unstructured.Unstructured) Session {
	s := Session{Name: o.GetName(), Created: o.GetCreationTimestamp().Time}
	ready := condition(o, "Ready")
	s.Ready, s.Reason, s.Message = ready.Ready(), ready.Reason, ready.Message
	if v := str(o, "spec", "lifecycle", "shutdownTime"); v != "" {
		s.Until, _ = time.Parse(time.RFC3339, v)
	}
	return s
}

// sessionLifecycle: end at until, and leave nothing behind.
func sessionLifecycle(until time.Time) map[string]any {
	return map[string]any{
		"shutdownTime":   until.UTC().Format(time.RFC3339),
		"shutdownPolicy": "Delete",
	}
}

// StartSession claims a new session for user and returns its name: name,
// or a generated session-xxxxx if name is "". A non-zero until ends it
// then. The pod isn't up yet (WaitSession).
func (c *Client) StartSession(ctx context.Context, user, name string, until time.Time) (string, error) {
	meta := map[string]any{"namespace": SandboxNamespace(user)}
	if name != "" {
		if err := CheckSessionName(name); err != nil {
			return "", err
		}
		meta["name"] = name
	} else {
		meta["generateName"] = "session-"
	}
	spec := map[string]any{"warmPoolRef": map[string]any{"name": sandboxPool}}
	if !until.IsZero() {
		spec["lifecycle"] = sessionLifecycle(until)
	}
	o, err := c.Dyn.Resource(gvrSandboxClaim).Namespace(SandboxNamespace(user)).Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxClaim",
		"metadata":   meta,
		"spec":       spec,
	}}, metav1.CreateOptions{FieldManager: FieldManager})
	if apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("there's a session %s already", name)
	}
	if err != nil {
		return "", err
	}
	return o.GetName(), nil
}

// Sessions lists user's sessions, oldest first.
func (c *Client) Sessions(ctx context.Context, user string) ([]Session, error) {
	l, err := c.Dyn.Resource(gvrSandboxClaim).Namespace(SandboxNamespace(user)).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(l.Items))
	for i := range l.Items {
		out = append(out, sessionOf(&l.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ErrNoSession: the user has no session of that name.
var ErrNoSession = errors.New("no such session")

// ErrSessionCantStart: a session isn't up, and waiting won't change that.
var ErrSessionCantStart = errors.New("can't start")

// GetSession returns user's session name, or ErrNoSession.
func (c *Client) GetSession(ctx context.Context, user, name string) (*Session, error) {
	o, err := c.Dyn.Resource(gvrSandboxClaim).Namespace(SandboxNamespace(user)).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%s: %w", name, ErrNoSession)
	}
	if err != nil {
		return nil, err
	}
	s := sessionOf(o)
	return &s, nil
}

// SetSessionEnd makes user's session end at until (zero: never).
func (c *Client) SetSessionEnd(ctx context.Context, user, name string, until time.Time) error {
	var lifecycle any
	if !until.IsZero() {
		lifecycle = sessionLifecycle(until)
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"lifecycle": lifecycle}})
	if err != nil {
		return err
	}
	_, err = c.Dyn.Resource(gvrSandboxClaim).Namespace(SandboxNamespace(user)).Patch(ctx, name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: FieldManager})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%s: %w", name, ErrNoSession)
	}
	return err
}

// StopSession ends user's session: its pod and everything in its work
// directory go.
func (c *Client) StopSession(ctx context.Context, user, name string) error {
	err := c.Dyn.Resource(gvrSandboxClaim).Namespace(SandboxNamespace(user)).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%s: %w", name, ErrNoSession)
	}
	return err
}

// sessionProblem says why a session that isn't ready won't become ready by
// waiting, or "" if it still may.
func (c *Client) sessionProblem(ctx context.Context, user string, s *Session) string {
	// The claim's pod was refused: the sandbox's limit on running sessions.
	if s.Reason == "ReconcilerError" {
		if strings.Contains(s.Message, "exceeded quota") {
			return "you have as many sessions running as your sandbox allows: stop one (mcloud sandbox stop), or ask an admin for more"
		}
		return firstNonEmpty(oneLine(s.Message), s.Reason)
	}
	p, err := c.Core.CoreV1().Pods(SandboxNamespace(user)).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, cs := range p.Status.ContainerStatuses {
		w := cs.State.Waiting
		if w == nil {
			continue
		}
		switch w.Reason {
		case "CreateContainerConfigError":
			return "it has no Claude token to start with: mcloud sandbox token set"
		case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
			return "the cluster can't pull the session image: " + oneLine(w.Message)
		}
	}
	return ""
}

// sessionWaiting describes what a session that isn't ready is waiting for.
func (c *Client) sessionWaiting(ctx context.Context, user, name string) string {
	p, err := c.Core.CoreV1().Pods(SandboxNamespace(user)).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "waiting for its pod"
	}
	if p.Spec.NodeName == "" {
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status != corev1.ConditionTrue && cond.Message != "" {
				return "waiting for room on a sandbox node: " + oneLine(cond.Message)
			}
		}
		return "waiting for a sandbox node"
	}
	return "starting on " + p.Spec.NodeName + " (the first session on a node pulls the image)"
}

// WaitSession waits until user's session is up (ctx bounds it), reporting
// what it's waiting for to status. It stops early on something waiting
// won't fix.
func (c *Client) WaitSession(ctx context.Context, user, name string, status func(string)) error {
	last := ""
	for {
		s, err := c.GetSession(ctx, user, name)
		if err != nil {
			return err
		}
		if s.Ready {
			return nil
		}
		if why := c.sessionProblem(ctx, user, s); why != "" {
			return fmt.Errorf("session %s %w: %s", name, ErrSessionCantStart, why)
		}
		if w := c.sessionWaiting(ctx, user, name); w != last && status != nil {
			status(w)
			last = w
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("session %s isn't up yet (%s): mcloud sandbox list shows when it is", name, firstNonEmpty(last, "waiting"))
		}
	}
}

// SessionExec runs cmd in user's session, in its work directory. On a
// terminal it's interactive.
func (c *Client) SessionExec(ctx context.Context, user, name string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return c.Exec(ctx, SandboxNamespace(user), name, SandboxContainer, cmd, stdin, stdout, stderr)
}
