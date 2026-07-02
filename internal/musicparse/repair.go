package musicparse

import "knox-media/internal/textencoding"

// RepairTrackMeta fixes mojibake in tag-derived metadata fields.
func RepairTrackMeta(meta TrackMeta) TrackMeta {
	meta.Title = textencoding.FixMetadataString(meta.Title)
	meta.Artist = textencoding.FixMetadataString(meta.Artist)
	meta.AlbumArtist = textencoding.FixMetadataString(meta.AlbumArtist)
	meta.Album = textencoding.FixMetadataString(meta.Album)
	meta.Genre = textencoding.FixMetadataString(meta.Genre)
	if meta.Album == "" {
		meta.Album = UnknownAlbum
	}
	if meta.AlbumArtist == "" {
		meta.AlbumArtist = pickAlbumArtist(meta.Artist, meta.Album)
	}
	if meta.Artist == "" {
		meta.Artist = meta.AlbumArtist
	}
	if meta.AlbumArtist == "" {
		meta.AlbumArtist = meta.Artist
	}
	if meta.AlbumArtist == "" {
		meta.AlbumArtist = VariousArtists
	}
	return meta
}
