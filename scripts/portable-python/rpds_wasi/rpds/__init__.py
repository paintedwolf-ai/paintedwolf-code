"""WASI-compatible pure-Python implementation of the rpds persistent data structures.

Provides HashTrieMap, HashTrieSet, List, and Queue with immutable semantics
compatible with rpds-py for CPython/WASI (wasm32-wasi) where dynamic native
extensions cannot be loaded.
"""

from __future__ import annotations

from collections.abc import Iterable, Iterator, Mapping, Set as AbstractSet
from types import GenericAlias
from typing import Any, Generic, TypeVar

__all__ = ["HashTrieMap", "HashTrieSet", "List", "Queue"]

_T = TypeVar("_T")
_K = TypeVar("_K")
_V = TypeVar("_V")


class HashTrieMap(Mapping[_K, _V]):
    """Immutable mapping compatible with rpds.HashTrieMap."""

    __slots__ = ("_data", "_hash")

    def __init__(
        self,
        value: Mapping[_K, _V] | Iterable[tuple[_K, _V]] = (),
        **kwds: Any,
    ) -> None:
        if isinstance(value, HashTrieMap) and not kwds:
            data = value._data
        else:
            data = dict(value)
            if kwds:
                data.update(kwds)
        object.__setattr__(self, "_data", data)
        object.__setattr__(self, "_hash", None)

    @classmethod
    def _from_dict(cls, data: dict[Any, Any]) -> HashTrieMap[Any, Any]:
        obj = cls.__new__(cls)
        object.__setattr__(obj, "_data", data)
        object.__setattr__(obj, "_hash", None)
        return obj

    __class_getitem__ = classmethod(GenericAlias)

    def __getitem__(self, key: _K) -> _V:
        return self._data[key]

    def __iter__(self) -> Iterator[_K]:
        return iter(self._data)

    def __len__(self) -> int:
        return len(self._data)

    def __contains__(self, key: object) -> bool:
        return key in self._data

    def get(self, key: _K, default: Any = None) -> Any:
        return self._data.get(key, default)

    def keys(self):
        return self._data.keys()

    def values(self):
        return self._data.values()

    def items(self):
        return self._data.items()

    def insert(self, key: _K, val: _V) -> HashTrieMap[_K, _V]:
        new_data = self._data.copy()
        new_data[key] = val
        return self._from_dict(new_data)

    def remove(self, key: _K) -> HashTrieMap[_K, _V]:
        if key not in self._data:
            raise KeyError(key)
        new_data = self._data.copy()
        del new_data[key]
        return self._from_dict(new_data)

    def discard(self, key: _K) -> HashTrieMap[_K, _V]:
        if key not in self._data:
            return self
        new_data = self._data.copy()
        del new_data[key]
        return self._from_dict(new_data)

    def update(self, *args: Any, **kwds: Any) -> HashTrieMap[Any, Any]:
        if not args and not kwds:
            return self
        new_data = self._data.copy()
        for arg in args:
            if isinstance(arg, HashTrieMap):
                new_data.update(arg._data)
            else:
                new_data.update(arg)
        if kwds:
            new_data.update(kwds)
        return self._from_dict(new_data)

    @classmethod
    def convert(
        cls,
        value: Mapping[_K, _V] | Iterable[tuple[_K, _V]],
    ) -> HashTrieMap[_K, _V]:
        if isinstance(value, cls):
            return value
        return cls(value)

    @classmethod
    def fromkeys(cls, keys: Iterable[_K], value: _V = None) -> HashTrieMap[_K, _V]:  # type: ignore[assignment]
        return cls._from_dict(dict.fromkeys(keys, value))

    def __eq__(self, other: object) -> bool:
        if isinstance(other, HashTrieMap):
            return self._data == other._data
        if isinstance(other, Mapping):
            return self._data == dict(other)
        return NotImplemented

    def __hash__(self) -> int:
        h = self._hash
        if h is None:
            h = hash(frozenset(self._data.items()))
            object.__setattr__(self, "_hash", h)
        return h

    def __or__(self, other: Mapping[Any, Any]) -> HashTrieMap[Any, Any]:
        if isinstance(other, Mapping):
            return self.update(other)
        return NotImplemented

    def __ror__(self, other: Mapping[Any, Any]) -> HashTrieMap[Any, Any]:
        if isinstance(other, Mapping):
            return HashTrieMap(other).update(self)
        return NotImplemented

    def __repr__(self) -> str:
        return f"HashTrieMap({self._data!r})"

    def __copy__(self) -> HashTrieMap[_K, _V]:
        return self

    def __reduce__(self):
        return (HashTrieMap, (self._data,))


class HashTrieSet(AbstractSet[_T]):
    """Immutable set compatible with rpds.HashTrieSet."""

    __slots__ = ("_data",)

    def __init__(self, value: Iterable[_T] = ()) -> None:
        if isinstance(value, HashTrieSet):
            data = value._data
        else:
            data = frozenset(value)
        object.__setattr__(self, "_data", data)

    @classmethod
    def _from_frozenset(cls, data: frozenset[Any]) -> HashTrieSet[Any]:
        obj = cls.__new__(cls)
        object.__setattr__(obj, "_data", data)
        return obj

    __class_getitem__ = classmethod(GenericAlias)

    def __iter__(self) -> Iterator[_T]:
        return iter(self._data)

    def __len__(self) -> int:
        return len(self._data)

    def __contains__(self, value: object) -> bool:
        return value in self._data

    def insert(self, value: _T) -> HashTrieSet[_T]:
        if value in self._data:
            return self
        return self._from_frozenset(self._data | {value})

    def remove(self, value: _T) -> HashTrieSet[_T]:
        if value not in self._data:
            raise KeyError(value)
        return self._from_frozenset(self._data - {value})

    def discard(self, value: _T) -> HashTrieSet[_T]:
        if value not in self._data:
            return self
        return self._from_frozenset(self._data - {value})

    def update(self, *args: Iterable[_T]) -> HashTrieSet[_T]:
        if not args:
            return self
        data = self._data.union(*args)
        return self._from_frozenset(data)

    def union(self, *others: Iterable[_T]) -> HashTrieSet[_T]:
        return self._from_frozenset(self._data.union(*others))

    def intersection(self, *others: Iterable[Any]) -> HashTrieSet[_T]:
        return self._from_frozenset(self._data.intersection(*others))

    def difference(self, *others: Iterable[Any]) -> HashTrieSet[_T]:
        return self._from_frozenset(self._data.difference(*others))

    def symmetric_difference(self, other: Iterable[_T]) -> HashTrieSet[_T]:
        return self._from_frozenset(self._data.symmetric_difference(other))

    def issubset(self, other: Iterable[Any]) -> bool:
        return self._data.issubset(other)

    def issuperset(self, other: Iterable[Any]) -> bool:
        return self._data.issuperset(other)

    def isdisjoint(self, other: Iterable[Any]) -> bool:
        return self._data.isdisjoint(other)

    def __or__(self, other: AbstractSet[Any]) -> HashTrieSet[Any]:
        if isinstance(other, AbstractSet):
            return self._from_frozenset(self._data | other)
        return NotImplemented

    def __and__(self, other: AbstractSet[Any]) -> HashTrieSet[_T]:
        if isinstance(other, AbstractSet):
            return self._from_frozenset(self._data & other)
        return NotImplemented

    def __sub__(self, other: AbstractSet[Any]) -> HashTrieSet[_T]:
        if isinstance(other, AbstractSet):
            return self._from_frozenset(self._data - other)
        return NotImplemented

    def __xor__(self, other: AbstractSet[Any]) -> HashTrieSet[Any]:
        if isinstance(other, AbstractSet):
            return self._from_frozenset(self._data ^ other)
        return NotImplemented

    def __eq__(self, other: object) -> bool:
        if isinstance(other, HashTrieSet):
            return self._data == other._data
        if isinstance(other, (set, frozenset, AbstractSet)):
            return self._data == other
        return NotImplemented

    def __hash__(self) -> int:
        return hash(self._data)

    def __repr__(self) -> str:
        return f"HashTrieSet({set(self._data)!r})"

    def __copy__(self) -> HashTrieSet[_T]:
        return self

    def __reduce__(self):
        return (HashTrieSet, (tuple(self._data),))


class List(Generic[_T]):
    """Immutable singly-linked list compatible with rpds.List."""

    __slots__ = ("_items",)

    def __init__(self, value: Iterable[_T] = (), *more: _T) -> None:
        items = tuple(value)
        if more:
            items = tuple(reversed(more)) + items
        object.__setattr__(self, "_items", items)

    @classmethod
    def _from_tuple(cls, items: tuple[Any, ...]) -> List[Any]:
        obj = cls.__new__(cls)
        object.__setattr__(obj, "_items", items)
        return obj

    __class_getitem__ = classmethod(GenericAlias)

    def __iter__(self) -> Iterator[_T]:
        return iter(self._items)

    def __len__(self) -> int:
        return len(self._items)

    def __bool__(self) -> bool:
        return bool(self._items)

    @property
    def first(self) -> _T:
        if not self._items:
            raise IndexError("empty list has no first element")
        return self._items[0]

    @property
    def rest(self) -> List[_T]:
        return self.drop_first()

    def push_front(self, value: _T) -> List[_T]:
        return self._from_tuple((value,) + self._items)

    def drop_first(self) -> List[_T]:
        if not self._items:
            raise IndexError("empty list has no first element")
        return self._from_tuple(self._items[1:])

    def reverse(self) -> List[_T]:
        return self._from_tuple(self._items[::-1])

    def __eq__(self, other: object) -> bool:
        if isinstance(other, List):
            return self._items == other._items
        return NotImplemented

    def __hash__(self) -> int:
        return hash(self._items)

    def __repr__(self) -> str:
        return f"List({list(self._items)!r})"

    def __copy__(self) -> List[_T]:
        return self

    def __reduce__(self):
        return (List, (self._items,))


class Queue(Generic[_T]):
    """Immutable FIFO queue compatible with rpds.Queue."""

    __slots__ = ("_items",)

    def __init__(self, value: Iterable[_T] = (), *more: _T) -> None:
        items = tuple(value)
        if more:
            items = items + more
        object.__setattr__(self, "_items", items)

    @classmethod
    def _from_tuple(cls, items: tuple[Any, ...]) -> Queue[Any]:
        obj = cls.__new__(cls)
        object.__setattr__(obj, "_items", items)
        return obj

    __class_getitem__ = classmethod(GenericAlias)

    def __iter__(self) -> Iterator[_T]:
        return iter(self._items)

    def __len__(self) -> int:
        return len(self._items)

    def __bool__(self) -> bool:
        return bool(self._items)

    @property
    def is_empty(self) -> bool:
        return not self._items

    @property
    def peek(self) -> _T:
        if not self._items:
            raise IndexError("empty queue has no peek element")
        return self._items[0]

    def enqueue(self, value: _T) -> Queue[_T]:
        return self._from_tuple(self._items + (value,))

    def dequeue(self) -> Queue[_T]:
        if not self._items:
            raise IndexError("cannot dequeue from an empty queue")
        return self._from_tuple(self._items[1:])

    def __eq__(self, other: object) -> bool:
        if isinstance(other, Queue):
            return self._items == other._items
        return NotImplemented

    def __hash__(self) -> int:
        return hash(self._items)

    def __repr__(self) -> str:
        return f"Queue({list(self._items)!r})"

    def __copy__(self) -> Queue[_T]:
        return self

    def __reduce__(self):
        return (Queue, (self._items,))
