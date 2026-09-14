"""
Reconstructed SQLAlchemy Models for MediaVault media_library plugin.
Extracted from app/models/media_library.pyc
"""

from datetime import datetime
from typing import Optional, Dict, List, Any
from sqlalchemy import (
    BigInteger, Boolean, DateTime, ForeignKey, Index, Integer, JSON, String, Text, UniqueConstraint
)
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column

class Base(DeclarativeBase):
    pass

class LibraryTimestamps:
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    updated_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow, onupdate=datetime.utcnow)
    deleted_at: Mapped[Optional[datetime]] = mapped_column(DateTime, nullable=True)

class MediaLibrary(Base, LibraryTimestamps):
    __tablename__ = "media_libraries"

    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    name: Mapped[str] = mapped_column(String(255), nullable=False)
    library_type: Mapped[str] = mapped_column(String(64), default="movies") # movies, tvshows, etc.
    source_type: Mapped[str] = mapped_column(String(64), default="local")
    storage_slug: Mapped[str] = mapped_column(String(64), default="")
    sort_order: Mapped[int] = mapped_column(Integer, default=0)
    root_path: Mapped[str] = mapped_column(Text, default="")
    root_paths: Mapped[List[str]] = mapped_column(JSON, default=list)
    allowed_user_ids: Mapped[List[str]] = mapped_column(JSON, default=list)
    scan_status: Mapped[str] = mapped_column(String(32), default="idle")
    scan_message: Mapped[str] = mapped_column(Text, default="")
    scan_settings: Mapped[Dict[str, Any]] = mapped_column(JSON, default=dict)
    scan_token: Mapped[str] = mapped_column(String(64), default="")
    notification_state: Mapped[Dict[str, Any]] = mapped_column(JSON, default=dict)
    cover_title: Mapped[str] = mapped_column(String(255), default="")
    cover_subtitle: Mapped[str] = mapped_column(String(255), default="")
    import_source: Mapped[str] = mapped_column(String(64), default="")
    last_scanned_at: Mapped[Optional[datetime]] = mapped_column(DateTime, nullable=True)
    default_sort: Mapped[str] = mapped_column(String(64), default="added")
    default_sort_order: Mapped[str] = mapped_column(String(16), default="desc")

class MediaLibraryItem(Base, LibraryTimestamps):
    __tablename__ = "media_library_items"

    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    library_id: Mapped[str] = mapped_column(String(64), ForeignKey("media_libraries.id"), nullable=False)
    parent_id: Mapped[Optional[str]] = mapped_column(String(64), ForeignKey("media_library_items.id"), nullable=True)
    identity_key: Mapped[str] = mapped_column(String(255), index=True) # e.g. jav:ABP-123 or tmdb:12345
    kind: Mapped[str] = mapped_column(String(32), nullable=False) # Movie, Series, Season, Episode
    title: Mapped[str] = mapped_column(String(512), nullable=False)
    title_initials: Mapped[str] = mapped_column(String(64), default="")
    year: Mapped[Optional[int]] = mapped_column(Integer, nullable=True)
    premiere_date: Mapped[Optional[datetime]] = mapped_column(DateTime, nullable=True)
    tmdb_id: Mapped[Optional[int]] = mapped_column(Integer, nullable=True)
    overview: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    genres: Mapped[List[str]] = mapped_column(JSON, default=list)
    poster_path: Mapped[Optional[str]] = mapped_column(String(1024), nullable=True)
    season: Mapped[Optional[int]] = mapped_column(Integer, nullable=True)
    episode: Mapped[Optional[int]] = mapped_column(Integer, nullable=True)
    duration_ticks: Mapped[Optional[int]] = mapped_column(BigInteger, default=0)
    is_missing: Mapped[bool] = mapped_column(Boolean, default=False)
    playback_markers: Mapped[Dict[str, Any]] = mapped_column(JSON, default=dict)
    detected_markers: Mapped[Dict[str, Any]] = mapped_column(JSON, default=dict)
    metadata_info: Mapped[Dict[str, Any]] = mapped_column(JSON, default=dict) # actors, director, tags, etc.
    metadata_locked: Mapped[bool] = mapped_column(Boolean, default=False)
    metadata_error: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    notified_at: Mapped[Optional[datetime]] = mapped_column(DateTime, nullable=True)
    content_added_at: Mapped[Optional[datetime]] = mapped_column(DateTime, nullable=True)

class MediaLibrarySource(Base, LibraryTimestamps):
    __tablename__ = "media_library_sources"

    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    library_id: Mapped[str] = mapped_column(String(64), ForeignKey("media_libraries.id"), nullable=False)
    item_id: Mapped[str] = mapped_column(String(64), ForeignKey("media_library_items.id"), nullable=False)
    source_key: Mapped[str] = mapped_column(String(255), default="")
    path: Mapped[str] = mapped_column(Text, nullable=False)
    stream_url: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    container: Mapped[str] = mapped_column(String(32), default="mkv")
    size: Mapped[int] = mapped_column(BigInteger, default=0)
    file_fingerprint: Mapped[str] = mapped_column(String(128), default="")
    probe_info: Mapped[Dict[str, Any]] = mapped_column(JSON, default=dict)
    media_streams: Mapped[List[Dict[str, Any]]] = mapped_column(JSON, default=list)
    subtitles: Mapped[List[Dict[str, Any]]] = mapped_column(JSON, default=list)
    error: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    is_missing: Mapped[bool] = mapped_column(Boolean, default=False)

class MediaLibraryUserData(Base):
    __tablename__ = "media_library_user_data"

    user_id: Mapped[str] = mapped_column(String(64), primary_key=True)
    item_id: Mapped[str] = mapped_column(String(64), ForeignKey("media_library_items.id"), primary_key=True)
    position_ticks: Mapped[int] = mapped_column(BigInteger, default=0)
    played: Mapped[bool] = mapped_column(Boolean, default=False)
    favorite: Mapped[bool] = mapped_column(Boolean, default=False)
    last_played_at: Mapped[Optional[datetime]] = mapped_column(DateTime, nullable=True)

class MediaLibrarySession(Base):
    __tablename__ = "media_library_sessions"

    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    user_id: Mapped[str] = mapped_column(String(64), ForeignKey("mv_users.id"), nullable=False)
    token_hash: Mapped[str] = mapped_column(String(128), index=True)
    user_session_version: Mapped[int] = mapped_column(Integer, default=1)
    device_name: Mapped[str] = mapped_column(String(255), default="")
    client_name: Mapped[str] = mapped_column(String(255), default="")
    device_id: Mapped[str] = mapped_column(String(255), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    expires_at: Mapped[Optional[datetime]] = mapped_column(DateTime, nullable=True)

class MediaLibraryScanTask(Base):
    __tablename__ = "media_library_scan_tasks"

    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    library_id: Mapped[str] = mapped_column(String(64), ForeignKey("media_libraries.id"), nullable=False)
    user_id: Mapped[Optional[str]] = mapped_column(String(64), nullable=True)
    status: Mapped[str] = mapped_column(String(32), default="pending")
    dedupe_key: Mapped[Optional[str]] = mapped_column(String(255), nullable=True)
    request: Mapped[Dict[str, Any]] = mapped_column(JSON, default=dict)
    checkpoint: Mapped[Dict[str, Any]] = mapped_column(JSON, default=dict)
    error: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
