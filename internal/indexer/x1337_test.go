package indexer

import "testing"

// A trimmed but structurally-faithful 1337x search results page.
const x1337ResultsHTML = `<html><body>
<table class="table-list table-responsive table-striped">
  <thead><tr><th class="coll-1 name">Name</th><th class="coll-2 seeds">SE</th></tr></thead>
  <tbody>
    <tr>
      <td class="coll-1 name">
        <a href="/sub/54/0/" class="icon"><i class="flaticon-film"></i></a>
        <a href="/torrent/5012345/The-Matrix-1999-1080p-BluRay-x264/">The Matrix 1999 1080p BluRay x264-GROUP</a>
      </td>
      <td class="coll-2 seeds">1,234</td>
      <td class="coll-3 leeches">56</td>
      <td class="coll-date">Jan. 1st '20</td>
      <td class="coll-4 size mob-vip">1.4 GB<span class="seeds mob-vip">1234</span></td>
      <td class="coll-5 uploader"><a href="/user/x/">someone</a></td>
    </tr>
    <tr>
      <td class="coll-1 name">
        <a href="/sub/54/0/" class="icon"></a>
        <a href="/torrent/6099999/Matrix-Reloaded-2003-2160p/">Matrix Reloaded 2003 2160p UHD BluRay</a>
      </td>
      <td class="coll-2 seeds">300</td>
      <td class="coll-3 leeches">12</td>
      <td class="coll-date">Feb. 2nd '21</td>
      <td class="coll-4 size mob-vip">45.2 GB<span class="seeds mob-vip">300</span></td>
      <td class="coll-5 uploader"><a href="/user/y/">other</a></td>
    </tr>
  </tbody>
</table>
</body></html>`

const x1337DetailHTML = `<html><body>
<ul class="download-links-dontblock">
  <li><a href="magnet:?xt=urn:btih:ABCDEF0123456789&dn=The.Matrix.1999.1080p.BluRay.x264-GROUP&tr=udp://tracker.example:80">Magnet Download</a></li>
</ul>
</body></html>`

func TestParseX1337Results(t *testing.T) {
	releases := parseX1337Results("https://1337x.to", x1337ResultsHTML)
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}

	r := releases[0]
	if r.Title != "The Matrix 1999 1080p BluRay x264-GROUP" {
		t.Errorf("title = %q", r.Title)
	}
	if r.DownloadURL != "https://1337x.to/torrent/5012345/The-Matrix-1999-1080p-BluRay-x264/" {
		t.Errorf("detail url = %q", r.DownloadURL)
	}
	if r.Seeders != 1234 || r.Peers != 56 {
		t.Errorf("seeders/peers = %d/%d, want 1234/56", r.Seeders, r.Peers)
	}
	// 1.4 GB, ignoring the trailing seed-count span.
	if got := r.SizeGB(); got < 1.39 || got > 1.41 {
		t.Errorf("size = %.2f GB, want ~1.4", got)
	}

	if releases[1].SizeBytes < 45*(1<<30) {
		t.Errorf("second size parsed wrong: %d", releases[1].SizeBytes)
	}
}

func TestParseX1337Magnet(t *testing.T) {
	got := parseX1337Magnet(x1337DetailHTML)
	want := "magnet:?xt=urn:btih:ABCDEF0123456789&dn=The.Matrix.1999.1080p.BluRay.x264-GROUP&tr=udp://tracker.example:80"
	if got != want {
		t.Errorf("magnet = %q\n want %q", got, want)
	}
	if parseX1337Magnet("<html><body>no magnet here</body></html>") != "" {
		t.Error("expected empty magnet when none present")
	}
}
