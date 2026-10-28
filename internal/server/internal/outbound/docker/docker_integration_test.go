//go:build integration

// Étage 2 de l'ADR-0010 : le démon Docker de la machine (ou du CI).
//
// Ce que le faux démon ne peut pas montrer : que le jeton écrit sur un stdin
// attaché avant le démarrage arrive VRAIMENT au processus, et que le
// durcissement demandé est VRAIMENT appliqué — racine en lecture seule, tmpfs
// exécutables, utilisateur nobody.

package docker_test

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/jijins1/warden/internal/server/internal/outbound/docker"
)

// dockerfileSonde : le conteneur réussit (code 0) seulement si tout ce que
// l'adaptateur promet est vrai à l'intérieur.
const dockerfileSonde = `FROM busybox:1.37
CMD ["sh", "-c", "\
read jeton; [ ${#jeton} -ge 32 ] || exit 10; \
[ \"$WARDEN_TOKEN_FILE\" = - ] || exit 11; \
touch /interdit 2>/dev/null && exit 12; \
[ \"$(id -u)\" = 65534 ] || exit 13; \
cp /bin/busybox /tmp/true && /tmp/true || exit 14; \
touch /workspace/ok || exit 15; \
case \"$(env)\" in *$jeton*) exit 16;; esac; \
exit 0"]
`

func TestIntegrationUnRunnerDemarreDurciAvecSonJeton(t *testing.T) {
	ctx := t.Context()
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("client Docker : %v", err)
	}
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Skipf("démon Docker indisponible : %v", err)
	}

	suffixe := fmt.Sprintf("%d", time.Now().UnixNano())
	img := "warden-sonde-runner:" + suffixe
	construire(t, cli, img)
	t.Cleanup(func() { _, _ = cli.ImageRemove(ctx, img, client.ImageRemoveOptions{Force: true}) })

	reseau := "warden-test-" + suffixe
	if _, err := cli.NetworkCreate(ctx, reseau, client.NetworkCreateOptions{Driver: "bridge"}); err != nil {
		t.Fatalf("réseau : %v", err)
	}
	t.Cleanup(func() { _, _ = cli.NetworkRemove(ctx, reseau, client.NetworkRemoveOptions{}) })

	d, err := docker.New(cli, docker.Config{
		Instance: "warden-test-" + suffixe, Network: reseau,
		DispatchURL: "http://wardend:8084", IngestURL: "http://wardend:8085",
	})
	if err != nil {
		t.Fatal(err)
	}

	s := spec(t, "warden-sonde-"+suffixe)
	s.Image = img
	if err := d.LaunchPod(ctx, s); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	t.Cleanup(func() { _ = d.DeletePod(ctx, s.Name) })

	limite := time.Now().Add(30 * time.Second)
	for time.Now().Before(limite) {
		faits, err := d.ObserverPods(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f := faits[s.Name]; f.Termine {
			if f.ExitCode != 0 {
				t.Fatalf("la sonde a échoué avec le code %d (voir dockerfileSonde)", f.ExitCode)
			}
			if f.ContainerReason != "Completed" {
				t.Errorf("raison : %q", f.ContainerReason)
			}
			if err := d.DeletePod(ctx, s.Name); err != nil {
				t.Errorf("DeletePod : %v", err)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("la sonde ne s'est pas terminée")
}

func construire(t *testing.T, cli *client.Client, tag string) {
	t.Helper()
	var contexte bytes.Buffer
	tw := tar.NewWriter(&contexte)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfileSonde))})
	_, _ = tw.Write([]byte(dockerfileSonde))
	_ = tw.Close()

	res, err := cli.ImageBuild(t.Context(), &contexte, client.ImageBuildOptions{Tags: []string{tag}, Remove: true})
	if err != nil {
		t.Skipf("construction de l'image sonde : %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	sortie, _ := io.ReadAll(res.Body)
	if bytes.Contains(sortie, []byte(`"errorDetail"`)) {
		t.Skipf("construction de l'image sonde : %s", sortie)
	}
}
