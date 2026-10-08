using System.Collections.ObjectModel;

namespace Plaitway.AppCore.ViewModels;

/// <summary>
/// Makes a collection that a list shows say what the model says, moving only the items that are not where they belong.
/// A list that is cleared and refilled loses the selection, the focus and a drag in progress; one that is edited in
/// place keeps them.
/// </summary>
public static class ObservableListSync
{
    /// <summary>
    /// Brings <paramref name="items"/> to the order and content of <paramref name="wanted"/>. Items are matched by
    /// <paramref name="keyOf"/>; one that is there already is kept, and <paramref name="update"/> tells it what changed.
    /// </summary>
    /// <typeparam name="TItem">What the list holds.</typeparam>
    /// <typeparam name="TSource">What the model offers.</typeparam>
    /// <typeparam name="TKey">What tells one item from another.</typeparam>
    /// <param name="items">The collection a list is bound to.</param>
    /// <param name="wanted">What it should hold, in order.</param>
    /// <param name="keyOf">The key of an item.</param>
    /// <param name="keyOfSource">The key of a source.</param>
    /// <param name="create">Makes an item for a source that has none.</param>
    /// <param name="update">Applies a source to the item that stands for it.</param>
    public static void Sync<TItem, TSource, TKey>(
        ObservableCollection<TItem> items,
        IReadOnlyList<TSource> wanted,
        Func<TItem, TKey> keyOf,
        Func<TSource, TKey> keyOfSource,
        Func<TSource, TItem> create,
        Action<TItem, TSource> update)
        where TKey : notnull
    {
        for (var index = 0; index < wanted.Count; index++)
        {
            var source = wanted[index];
            var key = keyOfSource(source);
            var existing = IndexOf(items, key, keyOf, index);
            if (existing < 0)
            {
                items.Insert(index, create(source));
            }
            else
            {
                if (existing != index)
                {
                    items.Move(existing, index);
                }

                update(items[index], source);
            }
        }

        while (items.Count > wanted.Count)
        {
            items.RemoveAt(items.Count - 1);
        }
    }

    private static int IndexOf<TItem, TKey>(ObservableCollection<TItem> items, TKey key, Func<TItem, TKey> keyOf, int from)
        where TKey : notnull
    {
        for (var index = from; index < items.Count; index++)
        {
            if (EqualityComparer<TKey>.Default.Equals(keyOf(items[index]), key))
            {
                return index;
            }
        }

        return -1;
    }
}
